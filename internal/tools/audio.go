package tools

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// ConvertToOpusOgg converts an audio file to Opus in an Ogg container and
// returns the path to the temporary output.
//
// These ffmpeg parameters match whatsapp-mcp-server/audio.py, which tunes the
// output for WhatsApp voice notes (24 kHz, 32k bitrate, VoIP mode, 60 ms frames).
func ConvertToOpusOgg(inputFile string) (string, error) {
	if _, err := os.Stat(inputFile); err != nil {
		return "", fmt.Errorf("input file not found: %s", inputFile)
	}

	out, err := os.CreateTemp("", "*.ogg")
	if err != nil {
		return "", err
	}
	outPath := out.Name()
	out.Close()

	cmd := exec.Command("ffmpeg",
		"-i", inputFile,
		"-c:a", "libopus",
		"-b:a", "32k",
		"-ar", "24000",
		"-application", "voip",
		"-vbr", "on",
		"-compression_level", "10",
		"-frame_duration", "60",
		"-y",
		outPath,
	)

	if output, err := cmd.CombinedOutput(); err != nil {
		os.Remove(outPath)
		return "", fmt.Errorf("failed to convert audio (you likely need ffmpeg installed): %v: %s", err, output)
	}

	return outPath, nil
}

// IsOpusOgg reports whether the path already has an .ogg extension.
func IsOpusOgg(path string) bool {
	return filepath.Ext(path) == ".ogg"
}
