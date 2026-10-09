// Package tools implements the WhatsApp queries and operations behind the MCP
// tools. It reads the local SQLite history and delegates sending and media
// download to the bridge.
//
// Output shapes mirror the original Python server in whatsapp-mcp-server/:
// the message-reading tools return formatted plain text (which the model
// consumes directly), while the chat and contact tools return structured data.
package tools

import (
	"database/sql"
	"fmt"
	"strings"
	"time"
)

// Message is one stored message.
type Message struct {
	Timestamp time.Time `json:"timestamp"`
	Sender    string    `json:"sender"`
	ChatName  string    `json:"chat_name,omitempty"`
	Content   string    `json:"content"`
	IsFromMe  bool      `json:"is_from_me"`
	ChatJID   string    `json:"chat_jid"`
	ID        string    `json:"id"`
	MediaType string    `json:"media_type,omitempty"`
}

// Chat is a conversation with its most recent message.
type Chat struct {
	JID             string     `json:"jid"`
	Name            string     `json:"name"`
	LastMessageTime *time.Time `json:"last_message_time"`
	LastMessage     string     `json:"last_message,omitempty"`
	LastSender      string     `json:"last_sender,omitempty"`
	LastIsFromMe    bool       `json:"last_is_from_me"`
}

// Contact is a person or business the account has chatted with.
type Contact struct {
	PhoneNumber string `json:"phone_number"`
	Name        string `json:"name"`
	JID         string `json:"jid"`
}

// MessageContext is a message plus its surrounding conversation.
type MessageContext struct {
	Message Message   `json:"message"`
	Before  []Message `json:"before"`
	After   []Message `json:"after"`
}

// Reader runs queries against the message history.
type Reader struct {
	db *sql.DB
}

// NewReader wraps the message store's database handle.
func NewReader(db *sql.DB) *Reader { return &Reader{db: db} }

// ---------------------------------------------------------------------------
// Formatting

// senderName resolves a display name for a sender, falling back to the raw JID.
func (r *Reader) senderName(senderJID string) string {
	var name sql.NullString
	err := r.db.QueryRow("SELECT name FROM chats WHERE jid = ? LIMIT 1", senderJID).Scan(&name)
	if err == nil && name.Valid && name.String != "" {
		return name.String
	}

	phonePart := senderJID
	if i := strings.Index(senderJID, "@"); i >= 0 {
		phonePart = senderJID[:i]
	}
	var name2 sql.NullString
	err = r.db.QueryRow("SELECT name FROM chats WHERE jid LIKE ? LIMIT 1", "%"+phonePart+"%").Scan(&name2)
	if err == nil && name2.Valid && name2.String != "" {
		return name2.String
	}

	return senderJID
}

// formatMessage renders one message exactly as the Python server did.
func (r *Reader) formatMessage(m Message, showChatInfo bool) string {
	var sb strings.Builder

	if showChatInfo && m.ChatName != "" {
		fmt.Fprintf(&sb, "[%s] Chat: %s ", m.Timestamp.Format("2006-01-02 15:04:05"), m.ChatName)
	} else {
		fmt.Fprintf(&sb, "[%s] ", m.Timestamp.Format("2006-01-02 15:04:05"))
	}

	prefix := ""
	if m.MediaType != "" {
		prefix = fmt.Sprintf("[%s - Message ID: %s - Chat JID: %s] ", m.MediaType, m.ID, m.ChatJID)
	}

	sender := "Me"
	if !m.IsFromMe {
		sender = r.senderName(m.Sender)
	}

	fmt.Fprintf(&sb, "From: %s: %s%s\n", sender, prefix, m.Content)
	return sb.String()
}

// formatMessagesList renders a list of messages, or a placeholder when empty.
func (r *Reader) formatMessagesList(messages []Message, showChatInfo bool) string {
	if len(messages) == 0 {
		return "No messages to display."
	}
	var sb strings.Builder
	for _, m := range messages {
		sb.WriteString(r.formatMessage(m, showChatInfo))
	}
	return sb.String()
}

// scanMessage reads a row in the canonical column order:
// timestamp, sender, chat_name, content, is_from_me, chat_jid, id, media_type.
func scanMessage(scan func(dest ...any) error) (Message, error) {
	var m Message
	var timestamp time.Time
	var chatName, mediaType sql.NullString
	err := scan(&timestamp, &m.Sender, &chatName, &m.Content, &m.IsFromMe, &m.ChatJID, &m.ID, &mediaType)
	if err != nil {
		return Message{}, err
	}
	m.Timestamp = timestamp
	m.ChatName = chatName.String
	m.MediaType = mediaType.String
	return m, nil
}
