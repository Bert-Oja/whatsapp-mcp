// Package wabridge owns the connection to WhatsApp: the whatsmeow client,
// event handling, history sync, media download and outbound sending.
package wabridge

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"time"

	"go.mau.fi/whatsmeow"
	"go.mau.fi/whatsmeow/proto/waE2E"
	"go.mau.fi/whatsmeow/store/sqlstore"
	"go.mau.fi/whatsmeow/types"
	"go.mau.fi/whatsmeow/types/events"
	waLog "go.mau.fi/whatsmeow/util/log"
	"google.golang.org/protobuf/proto"

	"github.com/Bert-Oja/whatsapp-mcp/internal/store"
)

// Bridge couples a live whatsmeow client with the local message store.
type Bridge struct {
	Client *whatsmeow.Client
	Store  *store.MessageStore
	Logger waLog.Logger

	// LoggedOut is closed when WhatsApp invalidates the session (the account
	// was unlinked, or the linked device expired). A headless deployment can
	// exit on this so the supervisor restarts it and prints a fresh QR code.
	LoggedOut     chan struct{}
	loggedOutOnce sync.Once
}

// Open connects to the WhatsApp session database under dir. The session
// (whatsapp.db) and the message history (messages.db) both live there.
func Open(dir string, logger waLog.Logger) (*Bridge, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}

	// Driver "sqlite" is modernc.org/sqlite (pure Go). whatsmeow accepts any
	// dialect string prefixed with "sqlite".
	//
	// busy_timeout and WAL matter here: whatsmeow touches this database from
	// many goroutines, and modernc.org/sqlite defaults busy_timeout to 0, so
	// contention immediately fails with SQLITE_BUSY (which surfaces as message
	// decryption failures). mattn/go-sqlite3 set this itself; modernc does not.
	//
	// Note the pragma syntax differs from mattn too: _pragma=foreign_keys(1)
	// rather than _foreign_keys=on.
	sessionDSN := "file:" + filepath.Join(dir, "whatsapp.db") +
		"?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
	container, err := sqlstore.New(context.Background(), "sqlite", sessionDSN, logger)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to session database: %w", err)
	}

	deviceStore, err := container.GetFirstDevice(context.Background())
	if err != nil {
		if err != sql.ErrNoRows {
			return nil, fmt.Errorf("failed to get device: %w", err)
		}
		deviceStore = container.NewDevice()
		logger.Infof("Created new device")
	}

	client := whatsmeow.NewClient(deviceStore, logger)
	if client == nil {
		return nil, fmt.Errorf("failed to create WhatsApp client")
	}

	messageStore, err := store.New(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize message store: %w", err)
	}

	b := &Bridge{
		Client:    client,
		Store:     messageStore,
		Logger:    logger,
		LoggedOut: make(chan struct{}),
	}
	client.AddEventHandler(b.handleEvent)
	return b, nil
}

// Close disconnects the client and closes the message store.
func (b *Bridge) Close() {
	if b.Client != nil {
		b.Client.Disconnect()
	}
	if b.Store != nil {
		b.Store.Close()
	}
}

func (b *Bridge) handleEvent(evt interface{}) {
	switch v := evt.(type) {
	case *events.Message:
		b.handleMessage(v)
	case *events.HistorySync:
		b.handleHistorySync(v)
	case *events.Connected:
		b.Logger.Infof("Connected to WhatsApp")
	case *events.LoggedOut:
		b.Logger.Warnf("Device logged out; the WhatsApp session is no longer valid and a new QR scan is required")
		b.loggedOutOnce.Do(func() { close(b.LoggedOut) })
	}
}

// ---------------------------------------------------------------------------
// Incoming

func (b *Bridge) handleMessage(msg *events.Message) {
	chatJID := msg.Info.Chat.String()
	sender := msg.Info.Sender.User

	name := b.ChatName(msg.Info.Chat, chatJID, nil, sender)

	if err := b.Store.StoreChat(chatJID, name, msg.Info.Timestamp); err != nil {
		b.Logger.Warnf("Failed to store chat: %v", err)
	}

	content := extractTextContent(msg.Message)
	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength := extractMediaInfo(msg.Message)

	if content == "" && mediaType == "" {
		return
	}

	err := b.Store.StoreMessage(
		msg.Info.ID, chatJID, sender, content, msg.Info.Timestamp, msg.Info.IsFromMe,
		mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	if err != nil {
		b.Logger.Warnf("Failed to store message: %v", err)
		return
	}

	timestamp := msg.Info.Timestamp.Format("2006-01-02 15:04:05")
	direction := "←"
	if msg.Info.IsFromMe {
		direction = "→"
	}

	if mediaType != "" {
		fmt.Printf("[%s] %s %s: [%s: %s] %s\n", timestamp, direction, sender, mediaType, filename, content)
	} else if content != "" {
		fmt.Printf("[%s] %s %s: %s\n", timestamp, direction, sender, content)
	}
}

func (b *Bridge) handleHistorySync(historySync *events.HistorySync) {
	fmt.Printf("Received history sync event with %d conversations\n", len(historySync.Data.Conversations))

	for _, conversation := range historySync.Data.Conversations {
		if conversation.ID == nil {
			continue
		}

		chatJID := *conversation.ID
		jid, err := types.ParseJID(chatJID)
		if err != nil {
			b.Logger.Warnf("Failed to parse JID %s: %v", chatJID, err)
			continue
		}

		name := b.ChatName(jid, chatJID, conversation, "")

		messages := conversation.Messages
		if len(messages) == 0 {
			continue
		}

		// The newest message determines the chat's last-message time.
		latestMsg := messages[0]
		if latestMsg == nil || latestMsg.Message == nil {
			continue
		}
		latestTS := time.Time{}
		if ts := latestMsg.Message.GetMessageTimestamp(); ts != 0 {
			latestTS = time.Unix(int64(ts), 0)
		} else {
			continue
		}
		b.Store.StoreChat(chatJID, name, latestTS)

		for _, msg := range messages {
			if msg == nil || msg.Message == nil {
				continue
			}

			var content string
			if msg.Message.Message != nil {
				if conv := msg.Message.Message.GetConversation(); conv != "" {
					content = conv
				} else if ext := msg.Message.Message.GetExtendedTextMessage(); ext != nil {
					content = ext.GetText()
				}
			}

			var mediaType, filename, url string
			var mediaKey, fileSHA256, fileEncSHA256 []byte
			var fileLength uint64
			if msg.Message.Message != nil {
				mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength = extractMediaInfo(msg.Message.Message)
			}

			if content == "" && mediaType == "" {
				continue
			}

			var sender string
			isFromMe := false
			if msg.Message.Key != nil {
				if msg.Message.Key.FromMe != nil {
					isFromMe = *msg.Message.Key.FromMe
				}
				switch {
				case isFromMe:
					sender = b.Client.Store.ID.User
				case msg.Message.Key.Participant != nil && *msg.Message.Key.Participant != "":
					sender = *msg.Message.Key.Participant
				default:
					sender = jid.User
				}
			} else {
				sender = jid.User
			}

			msgID := ""
			if msg.Message.Key != nil && msg.Message.Key.ID != nil {
				msgID = *msg.Message.Key.ID
			}

			timestamp := time.Time{}
			if ts := msg.Message.GetMessageTimestamp(); ts != 0 {
				timestamp = time.Unix(int64(ts), 0)
			} else {
				continue
			}

			if err := b.Store.StoreMessage(
				msgID, chatJID, sender, content, timestamp, isFromMe,
				mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
			); err != nil {
				b.Logger.Warnf("Failed to store history message: %v", err)
			}
		}
	}
}

// ChatName resolves a display name for a chat, preferring an already-stored
// name, then group/contact metadata, then fallbacks.
//
// conversation is normally nil. During history sync it carries the
// waHistorySync.Conversation proto, from which DisplayName/Name are read
// reflectively so this package does not depend on that proto type.
func (b *Bridge) ChatName(jid types.JID, chatJID string, conversation interface{}, sender string) string {
	if existing, err := b.Store.ChatName(chatJID); err == nil && existing != "" {
		return existing
	}

	var name string

	if jid.Server == "g.us" {
		if conversation != nil {
			var displayName, convName *string
			v := reflect.ValueOf(conversation)
			if v.Kind() == reflect.Ptr && !v.IsNil() {
				v = v.Elem()
				if f := v.FieldByName("DisplayName"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					s := f.Elem().String()
					displayName = &s
				}
				if f := v.FieldByName("Name"); f.IsValid() && f.Kind() == reflect.Ptr && !f.IsNil() {
					s := f.Elem().String()
					convName = &s
				}
			}
			if displayName != nil && *displayName != "" {
				name = *displayName
			} else if convName != nil && *convName != "" {
				name = *convName
			}
		}

		if name == "" {
			if groupInfo, err := b.Client.GetGroupInfo(context.Background(), jid); err == nil && groupInfo.Name != "" {
				name = groupInfo.Name
			} else {
				name = fmt.Sprintf("Group %s", jid.User)
			}
		}
	} else {
		contact, err := b.Client.Store.Contacts.GetContact(context.Background(), jid)
		switch {
		case err == nil && contact.FullName != "":
			name = contact.FullName
		case sender != "":
			name = sender
		default:
			name = jid.User
		}
	}

	return name
}

// ---------------------------------------------------------------------------
// Message content helpers

func extractTextContent(msg *waE2E.Message) string {
	if msg == nil {
		return ""
	}
	if text := msg.GetConversation(); text != "" {
		return text
	} else if ext := msg.GetExtendedTextMessage(); ext != nil {
		return ext.GetText()
	}
	return ""
}

func extractMediaInfo(msg *waE2E.Message) (mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) {
	if msg == nil {
		return "", "", "", nil, nil, nil, 0
	}

	if img := msg.GetImageMessage(); img != nil {
		return "image", "image_" + time.Now().Format("20060102_150405") + ".jpg",
			img.GetURL(), img.GetMediaKey(), img.GetFileSHA256(), img.GetFileEncSHA256(), img.GetFileLength()
	}
	if vid := msg.GetVideoMessage(); vid != nil {
		return "video", "video_" + time.Now().Format("20060102_150405") + ".mp4",
			vid.GetURL(), vid.GetMediaKey(), vid.GetFileSHA256(), vid.GetFileEncSHA256(), vid.GetFileLength()
	}
	if aud := msg.GetAudioMessage(); aud != nil {
		return "audio", "audio_" + time.Now().Format("20060102_150405") + ".ogg",
			aud.GetURL(), aud.GetMediaKey(), aud.GetFileSHA256(), aud.GetFileEncSHA256(), aud.GetFileLength()
	}
	if doc := msg.GetDocumentMessage(); doc != nil {
		name := doc.GetFileName()
		if name == "" {
			name = "document_" + time.Now().Format("20060102_150405")
		}
		return "document", name,
			doc.GetURL(), doc.GetMediaKey(), doc.GetFileSHA256(), doc.GetFileEncSHA256(), doc.GetFileLength()
	}

	return "", "", "", nil, nil, nil, 0
}

// ---------------------------------------------------------------------------
// Outgoing

// mediaTypeForExt maps a file extension to a WhatsApp media type and MIME type.
// Anything unrecognised becomes a document.
func mediaTypeForExt(ext string) (whatsmeow.MediaType, string) {
	switch strings.ToLower(ext) {
	case "jpg", "jpeg":
		return whatsmeow.MediaImage, "image/jpeg"
	case "png":
		return whatsmeow.MediaImage, "image/png"
	case "gif":
		return whatsmeow.MediaImage, "image/gif"
	case "webp":
		return whatsmeow.MediaImage, "image/webp"
	case "ogg":
		return whatsmeow.MediaAudio, "audio/ogg; codecs=opus"
	case "mp4":
		return whatsmeow.MediaVideo, "video/mp4"
	case "avi":
		return whatsmeow.MediaVideo, "video/avi"
	case "mov":
		return whatsmeow.MediaVideo, "video/quicktime"
	default:
		return whatsmeow.MediaDocument, "application/octet-stream"
	}
}

// SendMessage sends a text message, or a media message when mediaPath is set.
func (b *Bridge) SendMessage(ctx context.Context, recipient, message, mediaPath string) (bool, string) {
	client := b.Client
	if !client.IsConnected() {
		return false, "Not connected to WhatsApp"
	}

	var recipientJID types.JID
	var err error
	if strings.Contains(recipient, "@") {
		recipientJID, err = types.ParseJID(recipient)
		if err != nil {
			return false, fmt.Sprintf("Error parsing JID: %v", err)
		}
	} else {
		recipientJID = types.JID{User: recipient, Server: "s.whatsapp.net"}
	}

	msg := &waE2E.Message{}

	if mediaPath != "" {
		mediaData, err := os.ReadFile(mediaPath)
		if err != nil {
			return false, fmt.Sprintf("Error reading media file: %v", err)
		}

		ext := mediaPath[strings.LastIndex(mediaPath, ".")+1:]
		mediaType, mimeType := mediaTypeForExt(ext)

		resp, err := client.Upload(ctx, mediaData, mediaType)
		if err != nil {
			return false, fmt.Sprintf("Error uploading media: %v", err)
		}

		switch mediaType {
		case whatsmeow.MediaImage:
			msg.ImageMessage = &waE2E.ImageMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaAudio:
			var seconds uint32 = 30
			var waveform []byte
			if strings.Contains(mimeType, "ogg") {
				analyzedSeconds, analyzedWaveform, err := AnalyzeOggOpus(mediaData)
				if err != nil {
					return false, fmt.Sprintf("Failed to analyze Ogg Opus file: %v", err)
				}
				seconds = analyzedSeconds
				waveform = analyzedWaveform
			}
			msg.AudioMessage = &waE2E.AudioMessage{
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
				Seconds:       proto.Uint32(seconds),
				PTT:           proto.Bool(true),
				Waveform:      waveform,
			}
		case whatsmeow.MediaVideo:
			msg.VideoMessage = &waE2E.VideoMessage{
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		case whatsmeow.MediaDocument:
			msg.DocumentMessage = &waE2E.DocumentMessage{
				Title:         proto.String(mediaPath[strings.LastIndex(mediaPath, "/")+1:]),
				Caption:       proto.String(message),
				Mimetype:      proto.String(mimeType),
				URL:           &resp.URL,
				DirectPath:    &resp.DirectPath,
				MediaKey:      resp.MediaKey,
				FileEncSHA256: resp.FileEncSHA256,
				FileSHA256:    resp.FileSHA256,
				FileLength:    &resp.FileLength,
			}
		}
	} else {
		msg.Conversation = proto.String(message)
	}

	if _, err := client.SendMessage(ctx, recipientJID, msg); err != nil {
		return false, fmt.Sprintf("Error sending message: %v", err)
	}

	return true, fmt.Sprintf("Message sent to %s", recipient)
}

// ---------------------------------------------------------------------------
// Media download

// MediaDownloader implements whatsmeow.DownloadableMessage.
type MediaDownloader struct {
	URL           string
	DirectPath    string
	MediaKey      []byte
	FileLength    uint64
	FileSHA256    []byte
	FileEncSHA256 []byte
	MediaType     whatsmeow.MediaType
}

func (d *MediaDownloader) GetDirectPath() string             { return d.DirectPath }
func (d *MediaDownloader) GetURL() string                    { return d.URL }
func (d *MediaDownloader) GetMediaKey() []byte               { return d.MediaKey }
func (d *MediaDownloader) GetFileLength() uint64             { return d.FileLength }
func (d *MediaDownloader) GetFileSHA256() []byte             { return d.FileSHA256 }
func (d *MediaDownloader) GetFileEncSHA256() []byte          { return d.FileEncSHA256 }
func (d *MediaDownloader) GetMediaType() whatsmeow.MediaType { return d.MediaType }

// DownloadMedia fetches the media for a message, returning its type, filename
// and absolute local path. Already-downloaded files are returned directly.
func (b *Bridge) DownloadMedia(messageID, chatJID string) (bool, string, string, string, error) {
	chatDir := filepath.Join(b.Store.Dir(), strings.ReplaceAll(chatJID, ":", "_"))

	mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err := b.Store.GetMediaInfo(messageID, chatJID)
	if err != nil {
		// Older rows may not carry the extended media columns.
		mediaType, filename, err = b.Store.MediaInfoByName(messageID, chatJID)
		if err != nil {
			return false, "", "", "", fmt.Errorf("failed to find message: %v", err)
		}
	}

	if mediaType == "" {
		return false, "", "", "", fmt.Errorf("not a media message")
	}

	if err := os.MkdirAll(chatDir, 0o755); err != nil {
		return false, "", "", "", fmt.Errorf("failed to create chat directory: %v", err)
	}

	localPath := filepath.Join(chatDir, filename)
	absPath, err := filepath.Abs(localPath)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to get absolute path: %v", err)
	}

	if _, err := os.Stat(localPath); err == nil {
		return true, mediaType, filename, absPath, nil
	}

	if url == "" || len(mediaKey) == 0 || len(fileSHA256) == 0 || len(fileEncSHA256) == 0 || fileLength == 0 {
		return false, "", "", "", fmt.Errorf("incomplete media information for download")
	}

	var waMediaType whatsmeow.MediaType
	switch mediaType {
	case "image":
		waMediaType = whatsmeow.MediaImage
	case "video":
		waMediaType = whatsmeow.MediaVideo
	case "audio":
		waMediaType = whatsmeow.MediaAudio
	case "document":
		waMediaType = whatsmeow.MediaDocument
	default:
		return false, "", "", "", fmt.Errorf("unsupported media type: %s", mediaType)
	}

	downloader := &MediaDownloader{
		URL:           url,
		DirectPath:    extractDirectPathFromURL(url),
		MediaKey:      mediaKey,
		FileLength:    fileLength,
		FileSHA256:    fileSHA256,
		FileEncSHA256: fileEncSHA256,
		MediaType:     waMediaType,
	}

	mediaData, err := b.Client.Download(context.Background(), downloader)
	if err != nil {
		return false, "", "", "", fmt.Errorf("failed to download media: %v", err)
	}

	if err := os.WriteFile(localPath, mediaData, 0o644); err != nil {
		return false, "", "", "", fmt.Errorf("failed to save media file: %v", err)
	}

	return true, mediaType, filename, absPath, nil
}

// extractDirectPathFromURL pulls the /v/... path out of a WhatsApp media URL.
func extractDirectPathFromURL(rawURL string) string {
	parts := strings.SplitN(rawURL, ".net/", 2)
	if len(parts) < 2 {
		return rawURL
	}
	pathPart := strings.SplitN(parts[1], "?", 2)[0]
	return "/" + pathPart
}

// ---------------------------------------------------------------------------
// History

// RequestHistorySync asks WhatsApp for the first page of message history.
func (b *Bridge) RequestHistorySync(ctx context.Context) error {
	if b.Client == nil {
		return fmt.Errorf("client is not initialized")
	}
	if !b.Client.IsConnected() {
		return fmt.Errorf("client is not connected")
	}
	if b.Client.Store.ID == nil {
		return fmt.Errorf("client is not logged in")
	}

	historyMsg := b.Client.BuildHistorySyncRequest(nil, 100)
	if historyMsg == nil {
		return fmt.Errorf("failed to build history sync request")
	}

	_, err := b.Client.SendMessage(ctx, types.JID{Server: "s.whatsapp.net", User: "status"}, historyMsg)
	return err
}
