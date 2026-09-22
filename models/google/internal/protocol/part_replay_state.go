package protocol

import (
	"slices"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// partReplayState carries only fields without a Core content owner.
type partReplayState struct {
	ThoughtSignature   []byte                     `json:"thoughtSignature,omitempty"`
	MediaResolution    *genai.PartMediaResolution `json:"mediaResolution,omitzero"`
	VideoMetadata      *genai.VideoMetadata       `json:"videoMetadata,omitzero"`
	PartMetadata       map[string]any             `json:"partMetadata,omitempty"`
	AudioTranscription *genai.Transcription       `json:"audioTranscription,omitzero"`
	MediaProcessing    genai.MediaProcessing      `json:"mediaProcessing,omitempty"`
}

func newPartReplayState(part *genai.Part, kind corechat.PartKind) partReplayState {
	state := partReplayState{MediaResolution: part.MediaResolution, VideoMetadata: part.VideoMetadata,
		PartMetadata: part.PartMetadata, AudioTranscription: part.AudioTranscription, MediaProcessing: part.MediaProcessing}
	if kind != corechat.PartReasoning {
		state.ThoughtSignature = slices.Clone(part.ThoughtSignature)
	}
	return state
}

func (p partReplayState) apply(part *genai.Part) {
	if !part.Thought {
		part.ThoughtSignature = slices.Clone(p.ThoughtSignature)
	}
	part.MediaResolution = p.MediaResolution
	part.VideoMetadata = p.VideoMetadata
	part.PartMetadata = p.PartMetadata
	part.AudioTranscription = p.AudioTranscription
	part.MediaProcessing = p.MediaProcessing
}
