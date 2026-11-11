package hume

import (
	"fmt"

	"github.com/Tangerg/scope/core/speech"
)

const (
	streamEventAudio = "audio"
	audioFormatMP3   = "mp3"
	audioFormatWAV   = "wav"
	audioFormatPCM   = "pcm"
)

// audioFormatMIME names the media type of each audio format Hume returns.
func audioFormatMIME(format string) (string, error) {
	switch format {
	case audioFormatMP3:
		return "audio/mpeg", nil
	case audioFormatWAV:
		return "audio/wav", nil
	case audioFormatPCM:
		return "audio/pcm", nil
	default:
		return "", fmt.Errorf("%w: hume: unknown audio format %q", speech.ErrInvalidResponse, format)
	}
}

type snippetProgress struct {
	next     int64
	finished bool
}

// A single Core output cannot combine snippets from different requests or generations.
type generationStream struct {
	requestID    string
	generationID string
	audioFormat  string
	snippets     map[string]snippetProgress
}

func (g *generationStream) accept(event ttsStreamEvent) error {
	if event.ChunkIndex == nil || event.IsLastChunk == nil {
		return fmt.Errorf("%w: hume: streamed audio requires chunk_index and is_last_chunk", speech.ErrInvalidResponse)
	}
	if event.SnippetID == "" || event.GenerationID == "" || event.RequestID == "" {
		return fmt.Errorf("%w: hume: streamed audio is missing snippet identity", speech.ErrInvalidResponse)
	}
	if _, err := audioFormatMIME(event.AudioFormat); err != nil {
		return err
	}
	if event.UtteranceIndex != nil && *event.UtteranceIndex != 0 {
		return fmt.Errorf("%w: hume: streamed audio refers to unrequested utterance %d", speech.ErrInvalidResponse, *event.UtteranceIndex)
	}
	if g.requestID == "" {
		g.requestID, g.generationID, g.audioFormat = event.RequestID, event.GenerationID, event.AudioFormat
	}
	if event.RequestID != g.requestID || event.GenerationID != g.generationID || event.AudioFormat != g.audioFormat {
		return fmt.Errorf("%w: hume: streamed audio changed request, generation or format", speech.ErrInvalidResponse)
	}
	progress := g.snippets[event.SnippetID]
	if progress.finished || *event.ChunkIndex != progress.next {
		return fmt.Errorf("%w: hume: streamed snippet has inconsistent order or completion", speech.ErrInvalidResponse)
	}
	progress.next++
	progress.finished = *event.IsLastChunk
	g.snippets[event.SnippetID] = progress
	return nil
}

func (g *generationStream) complete() error {
	if len(g.snippets) == 0 {
		return fmt.Errorf("%w: hume: stream ended before any audio snippet arrived", speech.ErrInvalidResponse)
	}
	for _, progress := range g.snippets {
		if !progress.finished {
			return fmt.Errorf("%w: hume: stream ended before every snippet completed", speech.ErrInvalidResponse)
		}
	}
	return nil
}
