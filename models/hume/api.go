package hume

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/core/metadata"
	tts "github.com/Tangerg/scope/core/speech"
)

const maximumErrorResponseBytes = int64(64 * 1024)

type apiConfig struct {
	APIKey     string
	BaseURL    string
	HTTPClient *http.Client
}

func (a apiConfig) validate() error {
	if a.APIKey == "" {
		return errors.New("hume: APIKey is required")
	}
	return nil
}

type api struct {
	http *resty.Client
}

func newAPI(config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}
	client := resty.New()
	if config.HTTPClient != nil {
		client = resty.NewWithClient(config.HTTPClient)
	}
	client.SetBaseURL(cmp.Or(config.BaseURL, DefaultBaseURL)).
		SetHeader("X-Hume-API-Key", config.APIKey).
		SetHeader("Content-Type", "application/json")
	return &api{http: client}, nil
}

// voice references a named Octave voice. Provider is "HUME_AI" /
// "CUSTOM_VOICE" depending on where the voice is stored.
type voice struct {
	ID       string `json:"id,omitempty"`
	Name     string `json:"name,omitempty"`
	Provider string `json:"provider,omitempty"`
}

// Utterance is the per-segment input to TTS. "Description" is the
// emotion / style cue Octave is famous for (e.g. "calm, professional");
// it can replace Voice for fully-prompt-driven generation.
type utterance struct {
	Text            string   `json:"text"`
	Description     string   `json:"description,omitempty"`
	Voice           *voice   `json:"voice,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
	TrailingSilence *float64 `json:"trailing_silence,omitempty"`
}

// TTSRequest mirrors POST /tts. Format is "mp3" / "wav" / "pcm";
// SplitUtterances controls whether the response includes per-utterance
// timing.
type ttsRequest struct {
	Utterances            []utterance    `json:"utterances"`
	Context               map[string]any `json:"context,omitzero"`
	Format                map[string]any `json:"format,omitzero"`
	IncludeTimestampTypes []string       `json:"include_timestamp_types,omitempty"`
	NumGenerations        int            `json:"num_generations,omitempty"`
	SplitUtterances       *bool          `json:"split_utterances,omitempty"`
	StripHeaders          *bool          `json:"strip_headers,omitempty"`
	Temperature           *float64       `json:"temperature,omitempty"`
	Version               string         `json:"version,omitempty"`
	InstantMode           *bool          `json:"instant_mode,omitempty"`
}

// ttsResponse is the JSON envelope. Generations[0].Audio is the
// base64-encoded audio bytes.
type ttsResponse struct {
	Generations []struct {
		ID       string `json:"generation_id"`
		Audio    string `json:"audio"`
		Encoding struct {
			Format     string `json:"format"`
			SampleRate int    `json:"sample_rate"`
		} `json:"encoding"`
		Duration float64         `json:"duration"`
		FileSize int64           `json:"file_size"`
		Snippets json.RawMessage `json:"snippets,omitempty"`
	} `json:"generations"`
	RequestID string `json:"request_id"`
}

// ttsStreamEvent is one JSON-line union member returned by
// /tts/stream/json. Type is either "audio" or "timestamp".
type ttsStreamEvent struct {
	Type            string          `json:"type"`
	Audio           string          `json:"audio,omitempty"`
	AudioFormat     string          `json:"audio_format,omitempty"`
	ChunkIndex      int64           `json:"chunk_index,omitempty"`
	GenerationID    string          `json:"generation_id,omitempty"`
	IsLastChunk     bool            `json:"is_last_chunk,omitempty"`
	RequestID       string          `json:"request_id,omitempty"`
	Snippet         json.RawMessage `json:"snippet,omitempty"`
	SnippetID       string          `json:"snippet_id,omitempty"`
	Text            string          `json:"text,omitempty"`
	TranscribedText *string         `json:"transcribed_text,omitempty"`
	UtteranceIndex  *int64          `json:"utterance_index,omitempty"`
	Timestamp       json.RawMessage `json:"timestamp,omitempty"`
}

func (t *ttsStreamEvent) decodeAudio() ([]byte, error) {
	if t.Type != "audio" {
		return nil, fmt.Errorf("hume: stream event type %q has no audio", t.Type)
	}
	if t.Audio == "" {
		return nil, errors.New("hume: audio stream event has no audio")
	}
	audio, err := base64.StdEncoding.DecodeString(t.Audio)
	if err != nil {
		return nil, fmt.Errorf("hume: decode streamed audio: %w", err)
	}
	return audio, nil
}

func (t *ttsStreamEvent) response(model string) (*tts.Response, error) {
	audio, err := t.decodeAudio()
	if err != nil {
		return nil, err
	}
	var outputMetadata metadata.Map
	if setErr := outputMetadata.Set(metadataAudioFormat, t.AudioFormat); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataChunkIndex, t.ChunkIndex); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataGenerationID, t.GenerationID); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataIsLastChunk, t.IsLastChunk); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataSnippetID, t.SnippetID); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataText, t.Text); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataTranscribedText, t.TranscribedText); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataUtteranceIndex, t.UtteranceIndex); setErr != nil {
		return nil, setErr
	}
	if setErr := outputMetadata.Set(metadataStreamAudioEvent, t); setErr != nil {
		return nil, setErr
	}
	output, err := tts.NewOutput(audio, outputMetadata)
	if err != nil {
		return nil, err
	}
	responseMetadata := &tts.ResponseMetadata{Model: model}
	if t.RequestID != "" {
		if err := responseMetadata.Extra.Set(metadataRequestID, t.RequestID); err != nil {
			return nil, err
		}
	}
	return tts.NewResponse(output, responseMetadata)
}

// decodeAudio returns the raw audio bytes from the first generation.
func (t *ttsResponse) decodeAudio() ([]byte, error) {
	if len(t.Generations) == 0 {
		return nil, errors.New("hume: TTS response has no generations")
	}
	if t.Generations[0].Audio == "" {
		return nil, errors.New("hume: TTS response generation has no audio")
	}
	audio, err := base64.StdEncoding.DecodeString(t.Generations[0].Audio)
	if err != nil {
		return nil, fmt.Errorf("hume: decode TTS response audio: %w", err)
	}
	return audio, nil
}

// ttsStream starts the official streamed JSON-lines endpoint.
func (a *api) ttsStream(ctx context.Context, req *ttsRequest) (io.ReadCloser, error) {
	if req == nil {
		return nil, errors.New("hume: request must not be nil")
	}
	resp, err := a.http.R().
		SetContext(ctx).
		SetBody(req).
		SetDoNotParseResponse(true).
		Post("/tts/stream/json")
	if err != nil {
		return nil, fmt.Errorf("hume: stream request failed: %w", err)
	}
	if !resp.IsSuccess() {
		defer resp.RawBody().Close()
		body, readErr := io.ReadAll(io.LimitReader(resp.RawBody(), maximumErrorResponseBytes))
		if readErr != nil {
			return nil, fmt.Errorf("hume: stream http %d; read error response: %w", resp.StatusCode(), readErr)
		}
		return nil, fmt.Errorf("hume: stream http %d: %s", resp.StatusCode(), string(body))
	}
	if resp.RawBody() == nil {
		return nil, errors.New("hume: stream response has no body")
	}
	return resp.RawBody(), nil
}

func (a *api) tts(ctx context.Context, req *ttsRequest) (*ttsResponse, error) {
	if req == nil {
		return nil, errors.New("hume: request must not be nil")
	}
	var out ttsResponse
	resp, err := a.http.R().SetContext(ctx).SetBody(req).SetResult(&out).Post("/tts")
	if err != nil {
		return nil, fmt.Errorf("hume: request failed: %w", err)
	}
	if !resp.IsSuccess() {
		return nil, fmt.Errorf("hume: http %d: %s", resp.StatusCode(), resp.String())
	}
	return &out, nil
}
