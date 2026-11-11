package protocol

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"slices"

	"google.golang.org/genai"

	corechat "github.com/Tangerg/scope/core/chat"
)

// partReplayState carries only fields without a Core content owner.
type partReplayState struct {
	Before  serverToolParts `json:"before,omitempty"`
	Thought *bool           `json:"thought,omitzero"`
	// SignedReasoning records that a reasoning part's Core reasoning state is
	// a thought signature; the provider-namespaced key proves its issuer.
	SignedReasoning    bool                       `json:"signedReasoning,omitzero"`
	ThoughtSignature   []byte                     `json:"thoughtSignature,omitempty"`
	MediaResolution    *genai.PartMediaResolution `json:"mediaResolution,omitzero"`
	VideoMetadata      *genai.VideoMetadata       `json:"videoMetadata,omitzero"`
	PartMetadata       map[string]any             `json:"partMetadata,omitempty"`
	AudioTranscription *genai.Transcription       `json:"audioTranscription,omitzero"`
	MediaProcessing    genai.MediaProcessing      `json:"mediaProcessing,omitempty"`
	SpeechMetadata     *genai.SpeechMetadata      `json:"speechMetadata,omitzero"`
}

func newPartReplayState(part *genai.Part, kind corechat.PartKind, before serverToolParts) partReplayState {
	state := partReplayState{MediaResolution: part.MediaResolution, VideoMetadata: part.VideoMetadata,
		PartMetadata: part.PartMetadata, AudioTranscription: part.AudioTranscription, MediaProcessing: part.MediaProcessing,
		SpeechMetadata: part.SpeechMetadata, Before: before}
	if kind == corechat.PartToolCall || kind == corechat.PartMedia || kind == corechat.PartReasoning && part.Text == "" {
		state.Thought = new(part.Thought)
	}
	if kind == corechat.PartReasoning {
		state.SignedReasoning = len(part.ThoughtSignature) != 0
	} else {
		state.ThoughtSignature = slices.Clone(part.ThoughtSignature)
	}
	return state
}

func (p partReplayState) hasFields() bool {
	return len(p.Before) != 0 || p.Thought != nil || p.SignedReasoning || len(p.ThoughtSignature) != 0 ||
		p.MediaResolution != nil || p.VideoMetadata != nil || len(p.PartMetadata) != 0 ||
		p.AudioTranscription != nil || p.MediaProcessing != "" || p.SpeechMetadata != nil
}

func (p partReplayState) apply(part *genai.Part, kind corechat.PartKind) error {
	if kind == corechat.PartReasoning && len(p.ThoughtSignature) != 0 {
		return errors.New("reasoning signature is owned by Core reasoning state")
	}
	if kind != corechat.PartReasoning && p.SignedReasoning {
		return errors.New("only reasoning carries a signature in Core reasoning state")
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
	part.SpeechMetadata = p.SpeechMetadata
	return nil
}

func (p *partReplayState) UnmarshalJSON(data []byte) error {
	type wirePartReplayState partReplayState
	var decoded wirePartReplayState
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.RejectUnknownMembers(true), jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(unmarshalGenerationNumber))); err != nil {
		return err
	}
	var presence struct {
		Thought json.RawMessage `json:"thought"`
	}
	if err := jsonv2.Unmarshal(data, &presence); err != nil {
		return err
	}
	if jsontext.Value(presence.Thought).Kind() == jsontext.Null.Kind() {
		return errors.New("part replay thought flag must be a boolean")
	}
	if !partReplayState(decoded).hasFields() {
		return errors.New("part replay state has no native fields")
	}
	*p = partReplayState(decoded)
	return nil
}
