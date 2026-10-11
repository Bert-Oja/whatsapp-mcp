package tools

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// Sender is the WhatsApp-facing half the write tools need. It is implemented by
// *wabridge.Bridge.
type Sender interface {
	SendMessage(ctx context.Context, recipient, message, mediaPath string) (bool, string)
	DownloadMedia(messageID, chatJID string) (bool, string, string, string, error)
}

// SendMessageResult is the {success, message} pair the original tools returned.
type SendMessageResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// SendFileResult is the result of sending a media file.
type SendFileResult struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
}

// DownloadMediaResult is the result of downloading message media.
type DownloadMediaResult struct {
	Success  bool   `json:"success"`
	Message  string `json:"message"`
	Filename string `json:"filename,omitempty"`
	Path     string `json:"path,omitempty"`
}

// SendMessage sends a text message to a recipient.
func (r *Reader) SendMessage(ctx context.Context, s Sender, recipient, message string) (SendMessageResult, error) {
	if recipient == "" {
		return SendMessageResult{false, "Recipient must be provided"}, nil
	}

	success, msg := s.SendMessage(ctx, recipient, message, "")
	return SendMessageResult{success, msg}, nil
}

// SendFile sends a media file (image, video, audio or document).
func (r *Reader) SendFile(ctx context.Context, s Sender, recipient, mediaPath string) (SendFileResult, error) {
	if recipient == "" {
		return SendFileResult{false, "Recipient must be provided"}, nil
	}
	if mediaPath == "" {
		return SendFileResult{false, "Media path must be provided"}, nil
	}
	if _, err := os.Stat(mediaPath); err != nil {
		return SendFileResult{false, fmt.Sprintf("Media file not found: %s", mediaPath)}, nil
	}

	success, msg := s.SendMessage(ctx, recipient, "", mediaPath)
	return SendFileResult{success, msg}, nil
}

// SendAudioMessage sends an audio file as a voice note, converting it to Opus
// Ogg first when it is not already in that format.
func (r *Reader) SendAudioMessage(ctx context.Context, s Sender, recipient, mediaPath string) (SendFileResult, error) {
	if recipient == "" {
		return SendFileResult{false, "Recipient must be provided"}, nil
	}
	if mediaPath == "" {
		return SendFileResult{false, "Media path must be provided"}, nil
	}
	if _, err := os.Stat(mediaPath); err != nil {
		return SendFileResult{false, fmt.Sprintf("Media file not found: %s", mediaPath)}, nil
	}

	if !IsOpusOgg(mediaPath) {
		converted, err := ConvertToOpusOgg(mediaPath)
		if err != nil {
			return SendFileResult{
				false,
				fmt.Sprintf("Error converting file to opus ogg. You likely need to install ffmpeg: %v", err),
			}, nil
		}
		defer os.Remove(converted)
		mediaPath = converted
	}

	success, msg := s.SendMessage(ctx, recipient, "", mediaPath)
	return SendFileResult{success, msg}, nil
}

// DownloadMedia downloads the media attached to a message and reports its path.
func (r *Reader) DownloadMedia(ctx context.Context, s Sender, messageID, chatJID string) (DownloadMediaResult, error) {
	success, mediaType, filename, path, err := s.DownloadMedia(messageID, chatJID)
	if err != nil || !success {
		msg := "Unknown error"
		if err != nil {
			msg = err.Error()
		}
		return DownloadMediaResult{false, fmt.Sprintf("Failed to download media: %s", msg), "", ""}, nil
	}

	return DownloadMediaResult{
		Success:  true,
		Message:  fmt.Sprintf("Successfully downloaded %s media", mediaType),
		Filename: filename,
		Path:     path,
	}, nil
}

// FirstNonEmpty returns the first non-empty string, or "".
func FirstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
