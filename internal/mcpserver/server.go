// Package mcpserver exposes the WhatsApp operations as MCP tools.
package mcpserver

import (
	"context"
	"fmt"
	"sort"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/Bert-Oja/whatsapp-mcp/internal/tools"
)

// Options configures which tools are exposed.
type Options struct {
	// AllowWrites enables the tools that send messages and files. When false
	// (the default) the server exposes only read tools, and the write tools are
	// neither registered nor callable.
	AllowWrites bool
}

// Server holds the tool dependencies.
type Server struct {
	reader      *tools.Reader
	sender      tools.Sender
	allowWrites bool
}

// writeTools are the tools that mutate the outside world. They are only
// registered when writes are explicitly enabled.
var writeTools = map[string]bool{
	"send_message":       true,
	"send_file":          true,
	"send_audio_message": true,
}

// readOnlyTools is the set always available.
var readOnlyTools = map[string]bool{
	"search_contacts":            true,
	"list_messages":              true,
	"list_chats":                 true,
	"get_chat":                   true,
	"get_direct_chat_by_contact": true,
	"get_contact_chats":          true,
	"get_last_interaction":       true,
	"get_message_context":        true,
	"download_media":             true,
}

// New builds an MCP server. Read tools are always registered; the write tools
// are registered only when opts.AllowWrites is set.
//
// The sender is additionally wrapped in a guard, so that even if a write tool
// were reached in read-only mode the outbound call would still be refused.
func New(reader *tools.Reader, sender tools.Sender, opts Options) *mcp.Server {
	sender = newGuard(sender, opts.AllowWrites)
	s := &Server{reader: reader, sender: sender, allowWrites: opts.AllowWrites}
	srv := mcp.NewServer(&mcp.Implementation{
		Name:    "whatsapp",
		Version: "0.3.0",
	}, nil)

	readOnly := true
	destructive := true
	notDestructive := false

	// --- Read tools ---
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "search_contacts",
		Description: "Search WhatsApp contacts by name or phone number.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.searchContacts)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_messages",
		Description: "Get WhatsApp messages matching specified criteria with optional context.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listMessages)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "list_chats",
		Description: "Get WhatsApp chats matching specified criteria.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.listChats)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_chat",
		Description: "Get WhatsApp chat metadata by JID.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getChat)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_direct_chat_by_contact",
		Description: "Get WhatsApp chat metadata by sender phone number.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getDirectChatByContact)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_contact_chats",
		Description: "Get all WhatsApp chats involving the contact.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getContactChats)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_last_interaction",
		Description: "Get most recent WhatsApp message involving the contact.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getLastInteraction)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_message_context",
		Description: "Get context around a specific WhatsApp message.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.getMessageContext)
	mcp.AddTool(srv, &mcp.Tool{
		Name:        "download_media",
		Description: "Download media from a WhatsApp message and get the local file path.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, s.downloadMedia)

	// --- Write tools (opt-in) ---
	if opts.AllowWrites {
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "send_message",
			Description: "Send a WhatsApp message to a specified phone number or group JID.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: &destructive,
				IdempotentHint:  false,
				OpenWorldHint:   &readOnly,
			},
		}, s.sendMessage)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "send_file",
			Description: "Send a file (image, video, raw audio, document) to a specified recipient.",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: &destructive,
				IdempotentHint:  false,
				OpenWorldHint:   &readOnly,
			},
		}, s.sendFile)
		mcp.AddTool(srv, &mcp.Tool{
			Name:        "send_audio_message",
			Description: "Send an audio file as a WhatsApp voice message (requires an .ogg opus file, or ffmpeg for conversion).",
			Annotations: &mcp.ToolAnnotations{
				ReadOnlyHint:    false,
				DestructiveHint: &notDestructive,
				IdempotentHint:  false,
				OpenWorldHint:   &readOnly,
			},
		}, s.sendAudioMessage)
	}

	// Guard against the tool lists drifting apart.
	assertToolPartition()

	return srv
}

// assertToolPartition fails fast at construction if a tool is in both lists or
// in neither, which would silently expose or hide a capability.
func assertToolPartition() {
	for name := range writeTools {
		if readOnlyTools[name] {
			panic(fmt.Sprintf("mcpserver: tool %q is both read-only and write", name))
		}
	}
}

// WriteToolNames returns the write tool names, sorted. Intended for tests and
// for documenting what enabling writes exposes.
func WriteToolNames() []string {
	names := make([]string, 0, len(writeTools))
	for name := range writeTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// ReadOnlyToolNames returns the read tool names, sorted.
func ReadOnlyToolNames() []string {
	names := make([]string, 0, len(readOnlyTools))
	for name := range readOnlyTools {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// allowsWrites reports whether the write path is enabled.
func (s *Server) allowsWrites() bool { return s.allowWrites }

// ---------------------------------------------------------------------------
// Read tools

type searchContactsIn struct {
	Query string `json:"query" jsonschema:"Search term to match against contact names or phone numbers"`
}

func (s *Server) searchContacts(ctx context.Context, req *mcp.CallToolRequest, in searchContactsIn) (*mcp.CallToolResult, []tools.Contact, error) {
	contacts, err := s.reader.SearchContacts(in.Query)
	if err != nil {
		return nil, nil, err
	}
	return nil, contacts, nil
}

type listMessagesIn struct {
	After             string `json:"after,omitempty" jsonschema:"Optional ISO-8601 formatted string to only return messages after this date"`
	Before            string `json:"before,omitempty" jsonschema:"Optional ISO-8601 formatted string to only return messages before this date"`
	SenderPhoneNumber string `json:"sender_phone_number,omitempty" jsonschema:"Optional phone number to filter messages by sender"`
	ChatJID           string `json:"chat_jid,omitempty" jsonschema:"Optional chat JID to filter messages by chat"`
	Query             string `json:"query,omitempty" jsonschema:"Optional search term to filter messages by content"`
	Limit             int    `json:"limit,omitempty" jsonschema:"Maximum number of messages to return (default 20)"`
	Page              int    `json:"page,omitempty" jsonschema:"Page number for pagination (default 0)"`
	IncludeContext    *bool  `json:"include_context,omitempty" jsonschema:"Whether to include messages before and after matches (default true)"`
	ContextBefore     int    `json:"context_before,omitempty" jsonschema:"Number of messages to include before each match (default 1)"`
	ContextAfter      int    `json:"context_after,omitempty" jsonschema:"Number of messages to include after each match (default 1)"`
}

func (s *Server) listMessages(ctx context.Context, req *mcp.CallToolRequest, in listMessagesIn) (*mcp.CallToolResult, any, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	includeContext := true
	if in.IncludeContext != nil {
		includeContext = *in.IncludeContext
	}

	out, err := s.reader.ListMessages(tools.ListMessagesParams{
		After:             in.After,
		Before:            in.Before,
		SenderPhoneNumber: in.SenderPhoneNumber,
		ChatJID:           in.ChatJID,
		Query:             in.Query,
		Limit:             limit,
		Page:              in.Page,
		IncludeContext:    includeContext,
		ContextBefore:     in.ContextBefore,
		ContextAfter:      in.ContextAfter,
	})
	if err != nil {
		return nil, nil, err
	}
	return textResult(out), nil, nil
}

type listChatsIn struct {
	Query              string `json:"query,omitempty" jsonschema:"Optional search term to filter chats by name or JID"`
	Limit              int    `json:"limit,omitempty" jsonschema:"Maximum number of chats to return (default 20)"`
	Page               int    `json:"page,omitempty" jsonschema:"Page number for pagination (default 0)"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty" jsonschema:"Whether to include the last message in each chat (default true)"`
	SortBy             string `json:"sort_by,omitempty" jsonschema:"Field to sort results by, either last_active or name (default last_active)"`
}

func (s *Server) listChats(ctx context.Context, req *mcp.CallToolRequest, in listChatsIn) (*mcp.CallToolResult, []tools.Chat, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	includeLast := true
	if in.IncludeLastMessage != nil {
		includeLast = *in.IncludeLastMessage
	}
	sortBy := in.SortBy
	if sortBy == "" {
		sortBy = "last_active"
	}

	chats, err := s.reader.ListChats(tools.ListChatsParams{
		Query:              in.Query,
		Limit:              limit,
		Page:               in.Page,
		IncludeLastMessage: includeLast,
		SortBy:             sortBy,
	})
	if err != nil {
		return nil, nil, err
	}
	return nil, chats, nil
}

type getChatIn struct {
	ChatJID            string `json:"chat_jid" jsonschema:"The JID of the chat to retrieve"`
	IncludeLastMessage *bool  `json:"include_last_message,omitempty" jsonschema:"Whether to include the last message (default true)"`
}

func (s *Server) getChat(ctx context.Context, req *mcp.CallToolRequest, in getChatIn) (*mcp.CallToolResult, *tools.Chat, error) {
	includeLast := true
	if in.IncludeLastMessage != nil {
		includeLast = *in.IncludeLastMessage
	}
	chat, err := s.reader.GetChat(in.ChatJID, includeLast)
	if err != nil {
		return nil, nil, err
	}
	return nil, chat, nil
}

type getDirectChatIn struct {
	SenderPhoneNumber string `json:"sender_phone_number" jsonschema:"The phone number to search for"`
}

func (s *Server) getDirectChatByContact(ctx context.Context, req *mcp.CallToolRequest, in getDirectChatIn) (*mcp.CallToolResult, *tools.Chat, error) {
	chat, err := s.reader.GetDirectChatByContact(in.SenderPhoneNumber)
	if err != nil {
		return nil, nil, err
	}
	return nil, chat, nil
}

type getContactChatsIn struct {
	JID   string `json:"jid" jsonschema:"The contact's JID to search for"`
	Limit int    `json:"limit,omitempty" jsonschema:"Maximum number of chats to return (default 20)"`
	Page  int    `json:"page,omitempty" jsonschema:"Page number for pagination (default 0)"`
}

func (s *Server) getContactChats(ctx context.Context, req *mcp.CallToolRequest, in getContactChatsIn) (*mcp.CallToolResult, []tools.Chat, error) {
	limit := in.Limit
	if limit == 0 {
		limit = 20
	}
	chats, err := s.reader.GetContactChats(in.JID, limit, in.Page)
	if err != nil {
		return nil, nil, err
	}
	return nil, chats, nil
}

type getLastInteractionIn struct {
	JID string `json:"jid" jsonschema:"The JID of the contact to search for"`
}

func (s *Server) getLastInteraction(ctx context.Context, req *mcp.CallToolRequest, in getLastInteractionIn) (*mcp.CallToolResult, any, error) {
	out, err := s.reader.GetLastInteraction(in.JID)
	if err != nil {
		return nil, nil, err
	}
	return textResult(out), nil, nil
}

type getMessageContextIn struct {
	MessageID string `json:"message_id" jsonschema:"The ID of the message to get context for"`
	Before    int    `json:"before,omitempty" jsonschema:"Number of messages to include before the target message (default 5)"`
	After     int    `json:"after,omitempty" jsonschema:"Number of messages to include after the target message (default 5)"`
}

func (s *Server) getMessageContext(ctx context.Context, req *mcp.CallToolRequest, in getMessageContextIn) (*mcp.CallToolResult, tools.MessageContext, error) {
	before := in.Before
	if before == 0 {
		before = 5
	}
	after := in.After
	if after == 0 {
		after = 5
	}
	out, err := s.reader.GetMessageContext(in.MessageID, before, after)
	if err != nil {
		return nil, tools.MessageContext{}, err
	}
	return nil, out, nil
}

// textResult returns a tool result whose content is the raw string, so the
// model sees "No messages to display." rather than a JSON-encoded wrapper.
func textResult(text string) *mcp.CallToolResult {
	return &mcp.CallToolResult{
		Content: []mcp.Content{&mcp.TextContent{Text: text}},
	}
}

// ---------------------------------------------------------------------------
// Write tools

type sendMessageIn struct {
	Recipient string `json:"recipient" jsonschema:"Phone number or group JID to send to"`
	Message   string `json:"message" jsonschema:"The message text to send"`
}

func (s *Server) sendMessage(ctx context.Context, req *mcp.CallToolRequest, in sendMessageIn) (*mcp.CallToolResult, tools.SendMessageResult, error) {
	out, err := s.reader.SendMessage(ctx, s.sender, in.Recipient, in.Message)
	if err != nil {
		return nil, tools.SendMessageResult{}, err
	}
	return nil, out, nil
}

type sendFileIn struct {
	Recipient string `json:"recipient" jsonschema:"Phone number or group JID to send to"`
	MediaPath string `json:"media_path" jsonschema:"Path to the file to send"`
}

func (s *Server) sendFile(ctx context.Context, req *mcp.CallToolRequest, in sendFileIn) (*mcp.CallToolResult, tools.SendFileResult, error) {
	out, err := s.reader.SendFile(ctx, s.sender, in.Recipient, in.MediaPath)
	if err != nil {
		return nil, tools.SendFileResult{}, err
	}
	return nil, out, nil
}

type sendAudioMessageIn struct {
	Recipient string `json:"recipient" jsonschema:"Phone number or group JID to send to"`
	MediaPath string `json:"media_path" jsonschema:"Path to the audio file to send"`
}

func (s *Server) sendAudioMessage(ctx context.Context, req *mcp.CallToolRequest, in sendAudioMessageIn) (*mcp.CallToolResult, tools.SendFileResult, error) {
	out, err := s.reader.SendAudioMessage(ctx, s.sender, in.Recipient, in.MediaPath)
	if err != nil {
		return nil, tools.SendFileResult{}, err
	}
	return nil, out, nil
}

type downloadMediaIn struct {
	MessageID string `json:"message_id" jsonschema:"The ID of the message containing the media"`
	ChatJID   string `json:"chat_jid" jsonschema:"The JID of the chat containing the message"`
}

func (s *Server) downloadMedia(ctx context.Context, req *mcp.CallToolRequest, in downloadMediaIn) (*mcp.CallToolResult, tools.DownloadMediaResult, error) {
	out, err := s.reader.DownloadMedia(ctx, s.sender, in.MessageID, in.ChatJID)
	if err != nil {
		return nil, tools.DownloadMediaResult{}, err
	}
	return nil, out, nil
}
