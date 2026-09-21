package openai

import (
	"bytes"
	"fmt"
	"io"
	"mime"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/media"
)

func audioFile(value *media.Media) (io.Reader, error) {
	data, err := value.Bytes()
	if err != nil {
		return nil, err
	}
	name := value.Name
	if name == "" {
		mediaType, _, err := mime.ParseMediaType(value.MIME)
		if err != nil {
			return nil, err
		}
		var extension string
		switch mediaType {
		case "audio/mpeg", "audio/mp3":
			extension = ".mp3"
		case "audio/mp4", "video/mp4", "audio/x-m4a":
			extension = ".mp4"
		case "audio/wav", "audio/x-wav", "audio/wave":
			extension = ".wav"
		case "audio/webm", "video/webm":
			extension = ".webm"
		case "audio/ogg":
			extension = ".ogg"
		case "audio/flac", "audio/x-flac":
			extension = ".flac"
		default:
			return nil, fmt.Errorf("openai: audio filename is required for MIME type %q", value.MIME)
		}
		name = "audio" + extension
	}
	return openaisdk.File(bytes.NewReader(data), name, value.MIME), nil
}
