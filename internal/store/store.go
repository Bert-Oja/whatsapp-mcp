package store

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	// Pure-Go SQLite driver: no CGO, no glibc. Registered as "sqlite".
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
