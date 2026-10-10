package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Pure-Go SQLite driver: no CGO, no glibc. Registered as "sqlite".
	"go.mau.fi/whatsmeow/types"
	_ "modernc.org/sqlite"
)

// Message represents a stored chat message.
type Message struct {
	Time      time.Time
	Sender    string
	Content   string
	IsFromMe  bool
	MediaType string
	Filename  string
}

// TimeLayout is the format used to persist timestamps.
//
// This must match what mattn/go-sqlite3 wrote (its SQLiteTimestampFormats[0]),
// because the timestamp columns are compared lexicographically as text.
// modernc.org/sqlite would otherwise bind time.Time as
// "2006-01-02 15:04:05 +0000 UTC", which sorts incorrectly against existing
// rows and breaks every after/before filter and context query.
const TimeLayout = "2006-01-02 15:04:05.999999999-07:00"

// FormatTime renders a timestamp for storage or comparison.
func FormatTime(t time.Time) string {
	return t.Format(TimeLayout)
}

// DSN builds the modernc.org/sqlite connection string used for a database
// file. Exported so tests can assert the required pragmas are present.
func DSN(path string) string {
	return "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)"
}

// MessageStore owns the messages.db SQLite database.
type MessageStore struct {
	db  *sql.DB
	dir string
}

// New opens (creating if needed) the message database under dir.
func New(dir string) (*MessageStore, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create store directory: %w", err)
	}

	db, err := sql.Open("sqlite", DSN(filepath.Join(dir, "messages.db")))
	if err != nil {
		return nil, fmt.Errorf("failed to open message database: %w", err)
	}

	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS chats (
			jid TEXT PRIMARY KEY,
			name TEXT,
			last_message_time TIMESTAMP
		);

		CREATE TABLE IF NOT EXISTS messages (
			id TEXT,
			chat_jid TEXT,
			sender TEXT,
			content TEXT,
			timestamp TIMESTAMP,
			is_from_me BOOLEAN,
			media_type TEXT,
			filename TEXT,
			url TEXT,
			media_key BLOB,
			file_sha256 BLOB,
			file_enc_sha256 BLOB,
			file_length INTEGER,
			PRIMARY KEY (id, chat_jid),
			FOREIGN KEY (chat_jid) REFERENCES chats(jid)
		);
	`); err != nil {
		db.Close()
		return nil, fmt.Errorf("failed to create tables: %w", err)
	}

	return &MessageStore{db: db, dir: dir}, nil
}

// DB exposes the underlying handle for read queries.
func (ms *MessageStore) DB() *sql.DB { return ms.db }

// Dir returns the directory holding messages.db and downloaded media.
func (ms *MessageStore) Dir() string { return ms.dir }

// Close releases the database handle.
func (ms *MessageStore) Close() error { return ms.db.Close() }

// StoreChat inserts or updates a chat.
func (ms *MessageStore) StoreChat(jid, name string, lastMessageTime time.Time) error {
	_, err := ms.db.Exec(
		"INSERT OR REPLACE INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)",
		jid, name, FormatTime(lastMessageTime),
	)
	return err
}

// StoreMessage inserts or updates a message. Messages with neither content nor
// media are ignored.
func (ms *MessageStore) StoreMessage(id, chatJID, sender, content string, timestamp time.Time, isFromMe bool,
	mediaType, filename, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	if content == "" && mediaType == "" {
		return nil
	}

	_, err := ms.db.Exec(
		`INSERT OR REPLACE INTO messages
		(id, chat_jid, sender, content, timestamp, is_from_me, media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		id, chatJID, sender, content, FormatTime(timestamp), isFromMe, mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength,
	)
	return err
}

// GetMessages returns the most recent messages for a chat.
func (ms *MessageStore) GetMessages(chatJID string, limit int) ([]Message, error) {
	rows, err := ms.db.Query(
		"SELECT sender, content, timestamp, is_from_me, media_type, filename FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT ?",
		chatJID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var messages []Message
	for rows.Next() {
		var msg Message
		var timestamp time.Time
		if err := rows.Scan(&msg.Sender, &msg.Content, &timestamp, &msg.IsFromMe, &msg.MediaType, &msg.Filename); err != nil {
			return nil, err
		}
		msg.Time = timestamp
		messages = append(messages, msg)
	}

	return messages, rows.Err()
}

// GetChats returns every chat keyed by JID with its last message time.
func (ms *MessageStore) GetChats() (map[string]time.Time, error) {
	rows, err := ms.db.Query("SELECT jid, last_message_time FROM chats ORDER BY last_message_time DESC")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := make(map[string]time.Time)
	for rows.Next() {
		var jid string
		var lastMessageTime time.Time
		if err := rows.Scan(&jid, &lastMessageTime); err != nil {
			return nil, err
		}
		chats[jid] = lastMessageTime
	}

	return chats, rows.Err()
}

// StoreMediaInfo attaches download metadata to an existing message.
func (ms *MessageStore) StoreMediaInfo(id, chatJID, url string, mediaKey, fileSHA256, fileEncSHA256 []byte, fileLength uint64) error {
	_, err := ms.db.Exec(
		"UPDATE messages SET url = ?, media_key = ?, file_sha256 = ?, file_enc_sha256 = ?, file_length = ? WHERE id = ? AND chat_jid = ?",
		url, mediaKey, fileSHA256, fileEncSHA256, fileLength, id, chatJID,
	)
	return err
}

// GetMediaInfo reads the download metadata for a message.
func (ms *MessageStore) GetMediaInfo(id, chatJID string) (string, string, string, []byte, []byte, []byte, uint64, error) {
	var mediaType, filename, url string
	var mediaKey, fileSHA256, fileEncSHA256 []byte
	var fileLength uint64

	err := ms.db.QueryRow(
		"SELECT media_type, filename, url, media_key, file_sha256, file_enc_sha256, file_length FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename, &url, &mediaKey, &fileSHA256, &fileEncSHA256, &fileLength)

	return mediaType, filename, url, mediaKey, fileSHA256, fileEncSHA256, fileLength, err
}

// MediaInfoByName reads just the media type and filename for a message.
func (ms *MessageStore) MediaInfoByName(id, chatJID string) (string, string, error) {
	var mediaType, filename string
	err := ms.db.QueryRow(
		"SELECT media_type, filename FROM messages WHERE id = ? AND chat_jid = ?",
		id, chatJID,
	).Scan(&mediaType, &filename)
	return mediaType, filename, err
}

// ChatName returns the stored display name for a chat, if known.
func (ms *MessageStore) ChatName(chatJID string) (string, error) {
	var name string
	err := ms.db.QueryRow("SELECT name FROM chats WHERE jid = ?", chatJID).Scan(&name)
	return name, err
}

// ChatRow is a chat as stored in the database.
type ChatRow struct {
	JID             string
	Name            string
	LastMessageTime time.Time
}

// AllChats returns every stored chat. Used to re-resolve placeholder names
// after contact details arrive.
func (ms *MessageStore) AllChats() ([]ChatRow, error) {
	rows, err := ms.db.Query("SELECT jid, COALESCE(name, ''), last_message_time FROM chats")
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chats []ChatRow
	for rows.Next() {
		var c ChatRow
		if err := rows.Scan(&c.JID, &c.Name, &c.LastMessageTime); err != nil {
			return nil, err
		}
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// ErrNoAnchor reports that a chat has no stored message to anchor a history
// request on.
var ErrNoAnchor = errors.New("no stored message to anchor the request")

// NewestMessageInfo returns identity details for the most recent message in a
// chat, suitable as an anchor for an on-demand history request.
func (ms *MessageStore) NewestMessageInfo(chatJID string) (types.MessageInfo, error) {
	var (
		id     string
		sender string
		ts     time.Time
		fromMe bool
	)
	err := ms.db.QueryRow(
		"SELECT id, sender, timestamp, is_from_me FROM messages WHERE chat_jid = ? ORDER BY timestamp DESC LIMIT 1",
		chatJID,
	).Scan(&id, &sender, &ts, &fromMe)
	if err != nil {
		if err == sql.ErrNoRows {
			return types.MessageInfo{}, ErrNoAnchor
		}
		return types.MessageInfo{}, err
	}

	chat, perr := types.ParseJID(chatJID)
	if perr != nil {
		return types.MessageInfo{}, fmt.Errorf("parsing chat jid %q: %w", chatJID, perr)
	}
	senderJID, serr := types.ParseJID(sender)
	if serr != nil {
		// Senders are sometimes stored as a bare user or LID number.
		senderJID = types.JID{User: sender, Server: types.DefaultUserServer}
	}

	return types.MessageInfo{
		ID:        id,
		Timestamp: ts,
		MessageSource: types.MessageSource{
			IsFromMe: fromMe,
			Chat:     chat,
			Sender:   senderJID,
		},
	}, nil
}

// OldestMessageInfo returns identity details for the oldest message in a chat,
// used to walk history backwards one page at a time.
func (ms *MessageStore) OldestMessageInfo(chatJID string) (types.MessageInfo, error) {
	var (
		id     string
		sender string
		ts     time.Time
		fromMe bool
	)
	err := ms.db.QueryRow(
		"SELECT id, sender, timestamp, is_from_me FROM messages WHERE chat_jid = ? ORDER BY timestamp ASC LIMIT 1",
		chatJID,
	).Scan(&id, &sender, &ts, &fromMe)
	if err != nil {
		if err == sql.ErrNoRows {
			return types.MessageInfo{}, ErrNoAnchor
		}
		return types.MessageInfo{}, err
	}

	chat, perr := types.ParseJID(chatJID)
	if perr != nil {
		return types.MessageInfo{}, fmt.Errorf("parsing chat jid %q: %w", chatJID, perr)
	}
	senderJID, serr := types.ParseJID(sender)
	if serr != nil {
		senderJID = types.JID{User: sender, Server: types.DefaultUserServer}
	}

	return types.MessageInfo{
		ID:        id,
		Timestamp: ts,
		MessageSource: types.MessageSource{
			IsFromMe: fromMe,
			Chat:     chat,
			Sender:   senderJID,
		},
	}, nil
}

// MergeChat folds the chat at fromJID into the chat at toJID, then removes the
// source chat. The target is created if it does not exist.
//
// A message already present in the target with the same id is dropped rather
// than duplicated. Returns the number of messages that moved.
func (ms *MessageStore) MergeChat(fromJID, toJID string) (int, error) {
	if fromJID == toJID {
		return 0, nil
	}

	tx, err := ms.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var name string
	var lastTS string
	if err := tx.QueryRow(
		"SELECT COALESCE(name, ''), COALESCE(last_message_time, '') FROM chats WHERE jid = ?", toJID,
	).Scan(&name, &lastTS); err == sql.ErrNoRows {
		// Target does not exist yet: create it from the source's identity.
		if err := tx.QueryRow(
			"SELECT COALESCE(name, ''), COALESCE(last_message_time, '') FROM chats WHERE jid = ?", fromJID,
		).Scan(&name, &lastTS); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(
			"INSERT INTO chats (jid, name, last_message_time) VALUES (?, ?, ?)", toJID, name, lastTS,
		); err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}

	// OR IGNORE keeps the target's copy when an id already exists there.
	res, err := tx.Exec("UPDATE OR IGNORE messages SET chat_jid = ? WHERE chat_jid = ?", toJID, fromJID)
	if err != nil {
		return 0, err
	}
	moved, _ := res.RowsAffected()

	if _, err := tx.Exec("DELETE FROM messages WHERE chat_jid = ?", fromJID); err != nil {
		return 0, err
	}
	if _, err := tx.Exec("DELETE FROM chats WHERE jid = ?", fromJID); err != nil {
		return 0, err
	}

	// Recompute the surviving chat's last-message time.
	var newest string
	if err := tx.QueryRow(
		"SELECT COALESCE(MAX(timestamp), '') FROM messages WHERE chat_jid = ?", toJID,
	).Scan(&newest); err != nil {
		return 0, err
	}
	if newest != "" {
		if _, err := tx.Exec("UPDATE chats SET last_message_time = ? WHERE jid = ?", newest, toJID); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return int(moved), nil
}

// MessageCount returns how many messages are stored for a chat.
func (ms *MessageStore) MessageCount(chatJID string) (int, error) {
	var n int
	if err := ms.db.QueryRow("SELECT COUNT(*) FROM messages WHERE chat_jid = ?", chatJID).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// HistoryAnchor is a message identity recovered from a surviving store, used to
// bootstrap history when the local message database has no anchors.
type HistoryAnchor struct {
	ChatJID   string
	SenderJID string
	MessageID string
}

// LoadSurvivingAnchors reads message identities that survive a messages.db
// wipe.
//
// whatsmeow stores per-message secrets in whatsapp.db (the session database),
// which is kept across a reset. Each row carries a real chat JID, sender and
// message ID, so they can seed history requests for chats that would otherwise
// have no anchor at all.
//
// The session database is opened read-only and briefly; it is not the database
// this store owns.
func LoadSurvivingAnchors(sessionDBPath string) ([]HistoryAnchor, error) {
	db, err := sql.Open("sqlite", "file:"+sessionDBPath+"?mode=ro&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	defer db.Close()

	rows, err := db.Query(
		"SELECT chat_jid, sender_jid, message_id FROM whatsmeow_message_secrets ORDER BY rowid LIMIT 5000",
	)
	if err != nil {
		return nil, err // table may not exist on a fresh install
	}
	defer rows.Close()

	var anchors []HistoryAnchor
	for rows.Next() {
		var a HistoryAnchor
		if err := rows.Scan(&a.ChatJID, &a.SenderJID, &a.MessageID); err != nil {
			return nil, err
		}
		anchors = append(anchors, a)
	}
	return anchors, rows.Err()
}
