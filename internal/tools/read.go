package tools

import (
	"database/sql"
	"time"

	"github.com/Bert-Oja/whatsapp-mcp/internal/store"
)

// messageSelectColumns mirrors the Python server's projection so both servers
// read the history identically.
const messageSelectColumns = "messages.timestamp, messages.sender, chats.name, messages.content, messages.is_from_me, chats.jid, messages.id, messages.media_type"

// GetMessageContext returns the target message with up to before messages
// preceding it and after messages following it.
func (r *Reader) GetMessageContext(messageID string, before, after int) (MessageContext, error) {
	target, err := scanMessage(r.db.QueryRow(`
		SELECT `+messageSelectColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.id = ?`, messageID).Scan)
	if err != nil {
		if err == sql.ErrNoRows {
			return MessageContext{}, err
		}
		return MessageContext{}, err
	}

	beforeMsgs, err := r.contextRows(`
		SELECT `+messageSelectColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp < ?
		ORDER BY messages.timestamp DESC
		LIMIT ?`, target.ChatJID, store.FormatTime(target.Timestamp), before)
	if err != nil {
		return MessageContext{}, err
	}

	afterMsgs, err := r.contextRows(`
		SELECT `+messageSelectColumns+`
		FROM messages
		JOIN chats ON messages.chat_jid = chats.jid
		WHERE messages.chat_jid = ? AND messages.timestamp > ?
		ORDER BY messages.timestamp ASC
		LIMIT ?`, target.ChatJID, store.FormatTime(target.Timestamp), after)
	if err != nil {
		return MessageContext{}, err
	}

	return MessageContext{
		Message: target,
		Before:  beforeMsgs,
		After:   afterMsgs,
	}, nil
}

func (r *Reader) contextRows(query string, args ...any) ([]Message, error) {
	rows, err := r.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return nil, err
		}
		messages = append(messages, m)
	}
	return messages, rows.Err()
}

// ListMessagesParams mirrors the list_messages tool arguments.
type ListMessagesParams struct {
	After             string
	Before            string
	SenderPhoneNumber string
	ChatJID           string
	Query             string
	Limit             int
	Page              int
	IncludeContext    bool
	ContextBefore     int
	ContextAfter      int
}

// ListMessages returns formatted text for the messages matching the filters,
// optionally expanded with surrounding context per match.
func (r *Reader) ListMessages(p ListMessagesParams) (string, error) {
	where := []string{}
	args := []any{}

	if p.After != "" {
		t, err := parseISODate(p.After)
		if err != nil {
			return "", err
		}
		where = append(where, "messages.timestamp > ?")
		args = append(args, store.FormatTime(t))
	}
	if p.Before != "" {
		t, err := parseISODate(p.Before)
		if err != nil {
			return "", err
		}
		where = append(where, "messages.timestamp < ?")
		args = append(args, store.FormatTime(t))
	}
	if p.SenderPhoneNumber != "" {
		where = append(where, "messages.sender = ?")
		args = append(args, p.SenderPhoneNumber)
	}
	if p.ChatJID != "" {
		where = append(where, "messages.chat_jid = ?")
		args = append(args, p.ChatJID)
	}
	if p.Query != "" {
		where = append(where, "LOWER(messages.content) LIKE LOWER(?)")
		args = append(args, "%"+p.Query+"%")
	}

	query := "SELECT " + messageSelectColumns + " FROM messages JOIN chats ON messages.chat_jid = chats.jid"
	if len(where) > 0 {
		query += " WHERE " + joinAnd(where)
	}
	query += " ORDER BY messages.timestamp DESC LIMIT ? OFFSET ?"
	args = append(args, p.Limit, p.Page*p.Limit)

	rows, err := r.db.Query(query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	result := []Message{}
	for rows.Next() {
		m, err := scanMessage(rows.Scan)
		if err != nil {
			return "", err
		}
		result = append(result, m)
	}
	if err := rows.Err(); err != nil {
		return "", err
	}

	if p.IncludeContext && len(result) > 0 {
		withContext := []Message{}
		for _, m := range result {
			ctx, err := r.GetMessageContext(m.ID, p.ContextBefore, p.ContextAfter)
			if err != nil {
				// A message can disappear between queries; skip its context.
				continue
			}
			withContext = append(withContext, ctx.Before...)
			withContext = append(withContext, ctx.Message)
			withContext = append(withContext, ctx.After...)
		}
		return r.formatMessagesList(withContext, true), nil
	}

	return r.formatMessagesList(result, true), nil
}

// ListChatsParams mirrors the list_chats tool arguments.
type ListChatsParams struct {
	Query             string
	Limit             int
	Page              int
	IncludeLastMessage bool
	SortBy            string
}

// ListChats returns chats matching the filters.
func (r *Reader) ListChats(p ListChatsParams) ([]Chat, error) {
	selectParts := `SELECT chats.jid, chats.name, chats.last_message_time,
		messages.content as last_message, messages.sender as last_sender, messages.is_from_me as last_is_from_me
		FROM chats`

	if p.IncludeLastMessage {
		selectParts += ` LEFT JOIN messages ON chats.jid = messages.chat_jid AND chats.last_message_time = messages.timestamp`
	}

	where := []string{}
	args := []any{}
	if p.Query != "" {
		where = append(where, "(LOWER(chats.name) LIKE LOWER(?) OR chats.jid LIKE ?)")
		args = append(args, "%"+p.Query+"%", "%"+p.Query+"%")
	}
	if len(where) > 0 {
		selectParts += " WHERE " + joinAnd(where)
	}

	orderBy := "chats.last_message_time DESC"
	if p.SortBy != "last_active" {
		orderBy = "chats.name"
	}
	selectParts += " ORDER BY " + orderBy + " LIMIT ? OFFSET ?"
	args = append(args, p.Limit, p.Page*p.Limit)

	rows, err := r.db.Query(selectParts, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := []Chat{}
	for rows.Next() {
		var c Chat
		var lastTime sql.NullTime
		var lastMessage, lastSender sql.NullString
		var lastIsFromMe sql.NullBool
		if err := rows.Scan(&c.JID, &c.Name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
			return nil, err
		}
		if lastTime.Valid {
			t := lastTime.Time
			c.LastMessageTime = &t
		}
		c.LastMessage = lastMessage.String
		c.LastSender = lastSender.String
		c.LastIsFromMe = lastIsFromMe.Bool
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// GetChat returns one chat by JID.
func (r *Reader) GetChat(chatJID string, includeLastMessage bool) (*Chat, error) {
	query := `SELECT chats.jid, chats.name, chats.last_message_time,
		messages.content as last_message, messages.sender as last_sender, messages.is_from_me as last_is_from_me
		FROM chats`
	if includeLastMessage {
		query += ` LEFT JOIN messages ON chats.jid = messages.chat_jid AND chats.last_message_time = messages.timestamp`
	}
	query += " WHERE chats.jid = ?"

	return r.scanChat(r.db.QueryRow(query, chatJID).Scan)
}

// GetDirectChatByContact returns the first non-group chat matching the number.
func (r *Reader) GetDirectChatByContact(senderPhoneNumber string) (*Chat, error) {
	return r.scanChat(r.db.QueryRow(`
		SELECT chats.jid, chats.name, chats.last_message_time,
			messages.content as last_message, messages.sender as last_sender, messages.is_from_me as last_is_from_me
		FROM chats
		LEFT JOIN messages ON chats.jid = messages.chat_jid AND chats.last_message_time = messages.timestamp
		WHERE chats.jid LIKE ? AND chats.jid NOT LIKE '%@g.us'
		LIMIT 1`, "%"+senderPhoneNumber+"%").Scan)
}

func (r *Reader) scanChat(scan func(dest ...any) error) (*Chat, error) {
	var c Chat
	var lastTime sql.NullTime
	var lastMessage, lastSender sql.NullString
	var lastIsFromMe sql.NullBool
	if err := scan(&c.JID, &c.Name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	if lastTime.Valid {
		t := lastTime.Time
		c.LastMessageTime = &t
	}
	c.LastMessage = lastMessage.String
	c.LastSender = lastSender.String
	c.LastIsFromMe = lastIsFromMe.Bool
	return &c, nil
}

// SearchContacts finds contacts by name or phone number.
func (r *Reader) SearchContacts(query string) ([]Contact, error) {
	pattern := "%" + query + "%"
	rows, err := r.db.Query(`
		SELECT DISTINCT jid, name
		FROM chats
		WHERE (LOWER(name) LIKE LOWER(?) OR LOWER(jid) LIKE LOWER(?))
			AND jid NOT LIKE '%@g.us'
		ORDER BY name, jid
		LIMIT 50`, pattern, pattern)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	contacts := []Contact{}
	for rows.Next() {
		var jid string
		var name sql.NullString
		if err := rows.Scan(&jid, &name); err != nil {
			return nil, err
		}
		phone := jid
		if i := indexByte(jid, '@'); i >= 0 {
			phone = jid[:i]
		}
		contacts = append(contacts, Contact{PhoneNumber: phone, Name: name.String, JID: jid})
	}
	return contacts, rows.Err()
}

// GetContactChats lists chats that involve the contact.
func (r *Reader) GetContactChats(jid string, limit, page int) ([]Chat, error) {
	rows, err := r.db.Query(`
		SELECT DISTINCT c.jid, c.name, c.last_message_time,
			m.content as last_message, m.sender as last_sender, m.is_from_me as last_is_from_me
		FROM chats c
		JOIN messages m ON c.jid = m.chat_jid
		WHERE m.sender = ? OR c.jid = ?
		ORDER BY c.last_message_time DESC
		LIMIT ? OFFSET ?`, jid, jid, limit, page*limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	chats := []Chat{}
	for rows.Next() {
		var c Chat
		var lastTime sql.NullTime
		var lastMessage, lastSender sql.NullString
		var lastIsFromMe sql.NullBool
		if err := rows.Scan(&c.JID, &c.Name, &lastTime, &lastMessage, &lastSender, &lastIsFromMe); err != nil {
			return nil, err
		}
		if lastTime.Valid {
			t := lastTime.Time
			c.LastMessageTime = &t
		}
		c.LastMessage = lastMessage.String
		c.LastSender = lastSender.String
		c.LastIsFromMe = lastIsFromMe.Bool
		chats = append(chats, c)
	}
	return chats, rows.Err()
}

// GetLastInteraction returns the most recent message involving the contact,
// formatted as text. Returns "" when there is no interaction.
func (r *Reader) GetLastInteraction(jid string) (string, error) {
	m, err := scanMessage(r.db.QueryRow(`
		SELECT m.timestamp, m.sender, c.name, m.content, m.is_from_me, c.jid, m.id, m.media_type
		FROM messages m
		JOIN chats c ON m.chat_jid = c.jid
		WHERE m.sender = ? OR c.jid = ?
		ORDER BY m.timestamp DESC
		LIMIT 1`, jid, jid).Scan)
	if err != nil {
		if err == sql.ErrNoRows {
			return "", nil
		}
		return "", err
	}
	return r.formatMessage(m, true), nil
}

// ---------------------------------------------------------------------------

func parseISODate(value string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, value)
	if err == nil {
		return t, nil
	}
	// Accept the space-separated form Python's fromisoformat also allows.
	if t, err2 := time.Parse("2006-01-02 15:04:05", value); err2 == nil {
		return t, nil
	}
	return time.Time{}, err
}

func joinAnd(parts []string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += " AND "
		}
		out += p
	}
	return out
}

func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}
