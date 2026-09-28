package protocol

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// partReplayState carries only fields without a Core content owner.
type partReplayState struct {
	Thought            *bool                      `json:"thought"`
	PartIndex          *int                       `json:"partIndex,omitzero"`
	ThoughtSignature   []byte                     `json:"thoughtSignature,omitempty"`
	MediaResolution    *genai.PartMediaResolution `json:"mediaResolution,omitzero"`
	VideoMetadata      *genai.VideoMetadata       `json:"videoMetadata,omitzero"`
	PartMetadata       map[string]any             `json:"partMetadata,omitempty"`
	AudioTranscription *genai.Transcription       `json:"audioTranscription,omitzero"`
	MediaProcessing    genai.MediaProcessing      `json:"mediaProcessing,omitempty"`
}

func newPartReplayState(part *genai.Part, kind corechat.PartKind, partIndex int) partReplayState {
	state := partReplayState{Thought: new(part.Thought), MediaResolution: part.MediaResolution, VideoMetadata: part.VideoMetadata,
		PartMetadata: part.PartMetadata, AudioTranscription: part.AudioTranscription, MediaProcessing: part.MediaProcessing}
	// Each signature belongs to its original Part. Distinct signed Parts must
	// stay separate when Core aggregates adjacent text or reasoning chunks.
	if len(part.ThoughtSignature) != 0 {
		state.PartIndex = new(partIndex)
	}
	if kind != corechat.PartReasoning {
		state.ThoughtSignature = slices.Clone(part.ThoughtSignature)
	}
	return state
}

func (p partReplayState) apply(part *genai.Part, kind corechat.PartKind) {
	part.Thought = *p.Thought
	if kind != corechat.PartReasoning {
		part.ThoughtSignature = slices.Clone(p.ThoughtSignature)
	}
	part.MediaResolution = p.MediaResolution
	part.VideoMetadata = p.VideoMetadata
	part.PartMetadata = p.PartMetadata
	part.AudioTranscription = p.AudioTranscription
	part.MediaProcessing = p.MediaProcessing
}

func (p *partReplayState) UnmarshalJSON(data []byte) error {
	type wirePartReplayState partReplayState
	var decoded wirePartReplayState
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	if decoded.Thought == nil {
		return errors.New("part replay state requires the original thought flag")
	}
	if decoded.PartIndex != nil && *decoded.PartIndex < 0 {
		return errors.New("part replay index must not be negative")
	}
	*p = partReplayState(decoded)
	return nil
}
