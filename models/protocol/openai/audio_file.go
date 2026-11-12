package openai

import (
	"bytes"
	"fmt"
	"io"

	openaisdk "github.com/openai/openai-go/v3"

	"github.com/Tangerg/scope/core/media"
)

const (
	audioMIMEMPEG            = "audio/mpeg"
	audioMIMEMP3             = "audio/mp3"
	audioMIMEMP4             = "audio/mp4"
	videoMIMEMP4             = "video/mp4"
	audioMIMEXM4A            = "audio/x-m4a"
	audioMIMEWAV             = "audio/wav"
	audioMIMEXWAV            = "audio/x-wav"
	audioMIMEWave            = "audio/wave"
	audioMIMEWebM            = "audio/webm"
	videoMIMEWebM            = "video/webm"
	audioMIMEOgg             = "audio/ogg"
	audioMIMEFLAC            = "audio/flac"
	audioMIMEXFLAC           = "audio/x-flac"
	audioExtensionMP3        = ".mp3"
	audioExtensionMP4        = ".mp4"
	audioExtensionWAV        = ".wav"
	audioExtensionWebM       = ".webm"
	audioExtensionOgg        = ".ogg"
	audioExtensionFLAC       = ".flac"
	defaultAudioFilenameStem = "audio"
)

func audioFile(value *media.Media) (io.Reader, error) {
	data, err := value.Bytes()
	if err != nil {
		return nil, err
	}
	name := value.Name
	if name == "" {
		var extension string
		switch value.Type() {
		case audioMIMEMPEG, audioMIMEMP3:
			extension = audioExtensionMP3
		case audioMIMEMP4, videoMIMEMP4, audioMIMEXM4A:
			extension = audioExtensionMP4
		case audioMIMEWAV, audioMIMEXWAV, audioMIMEWave:
			extension = audioExtensionWAV
		case audioMIMEWebM, videoMIMEWebM:
			extension = audioExtensionWebM
		case audioMIMEOgg:
			extension = audioExtensionOgg
		case audioMIMEFLAC, audioMIMEXFLAC:
			extension = audioExtensionFLAC
		default:
			return nil, fmt.Errorf("openai: audio filename is required for MIME type %q", value.MIME)
		}
		name = defaultAudioFilenameStem + extension
	}
	return openaisdk.File(bytes.NewReader(data), name, value.MIME), nil
}
