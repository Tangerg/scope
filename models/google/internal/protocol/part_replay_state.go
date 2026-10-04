package protocol

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// partReplayState carries only fields without a Core content owner.
type partReplayState struct {
	Thought            *bool                      `json:"thought,omitzero"`
	PartIndex          *int                       `json:"partIndex,omitzero"`
	ThoughtSignature   []byte                     `json:"thoughtSignature,omitempty"`
	MediaResolution    *genai.PartMediaResolution `json:"mediaResolution,omitzero"`
	VideoMetadata      *genai.VideoMetadata       `json:"videoMetadata,omitzero"`
	PartMetadata       map[string]any             `json:"partMetadata,omitempty"`
	AudioTranscription *genai.Transcription       `json:"audioTranscription,omitzero"`
	MediaProcessing    genai.MediaProcessing      `json:"mediaProcessing,omitempty"`
}

func newPartReplayState(part *genai.Part, kind corechat.PartKind, partIndex int) partReplayState {
	state := partReplayState{MediaResolution: part.MediaResolution, VideoMetadata: part.VideoMetadata,
		PartMetadata: part.PartMetadata, AudioTranscription: part.AudioTranscription, MediaProcessing: part.MediaProcessing}
	if kind == corechat.PartToolCall || kind == corechat.PartMedia || kind == corechat.PartReasoning && part.Text == "" {
		state.Thought = new(part.Thought)
	}
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

func (p partReplayState) hasFields() bool {
	return p.Thought != nil || p.PartIndex != nil || len(p.ThoughtSignature) != 0 ||
		p.MediaResolution != nil || p.VideoMetadata != nil || len(p.PartMetadata) != 0 ||
		p.AudioTranscription != nil || p.MediaProcessing != ""
}

func (p partReplayState) apply(part *genai.Part, kind corechat.PartKind) error {
	if kind == corechat.PartReasoning && len(p.ThoughtSignature) != 0 {
		return errors.New("reasoning signature is owned by Core reasoning state")
	}
	if kind == corechat.PartText || kind == corechat.PartRefusal || kind == corechat.PartReasoning && part.Text != "" {
		if p.Thought != nil {
			return errors.New("visible text thought classification is owned by the Core part kind")
		}
	}
	if p.Thought != nil {
		part.Thought = *p.Thought
	}
	if kind != corechat.PartReasoning {
		part.ThoughtSignature = slices.Clone(p.ThoughtSignature)
	}
	part.MediaResolution = p.MediaResolution
	part.VideoMetadata = p.VideoMetadata
	part.PartMetadata = p.PartMetadata
	part.AudioTranscription = p.AudioTranscription
	part.MediaProcessing = p.MediaProcessing
	return nil
}

func (p *partReplayState) UnmarshalJSON(data []byte) error {
	type wirePartReplayState partReplayState
	var decoded wirePartReplayState
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	var presence struct {
		Thought json.RawMessage `json:"thought"`
	}
	if err := jsonv2.Unmarshal(data, &presence); err != nil {
		return err
	}
	if bytes.Equal(bytes.TrimSpace(presence.Thought), []byte("null")) {
		return errors.New("part replay thought flag must be a boolean")
	}
	if !partReplayState(decoded).hasFields() {
		return errors.New("part replay state has no native fields")
	}
	if decoded.PartIndex != nil && *decoded.PartIndex < 0 {
		return errors.New("part replay index must not be negative")
	}
	*p = partReplayState(decoded)
	return nil
}
