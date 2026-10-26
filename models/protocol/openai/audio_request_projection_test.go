package openai_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/protocol/openai"
)

func TestAudioRequestExtensionsPreserveMultipartFields(t *testing.T) {
	for _, test := range []struct {
		name      string
		modality  string
		model     string
		language  string
		extension string
		fields    map[string][]string
	}{
		{
			name: "transcription zero temperature and empty prompt", modality: "transcription", model: "gpt-4o-transcribe", language: "en",
			extension: `{"temperature":0,"prompt":"","response_format":"json","include":["logprobs"]}`,
			fields:    map[string][]string{"temperature": {"0"}, "prompt": {""}, "response_format": {"json"}, "include[]": {"logprobs"}},
		},
		{
			name: "transcription timestamp array", modality: "transcription", model: "whisper-1",
			extension: `{"response_format":"verbose_json","timestamp_granularities":["word","segment"]}`,
			fields:    map[string][]string{"response_format": {"verbose_json"}, "timestamp_granularities[]": {"word", "segment"}},
		},
		{
			name: "transcription language and keyword arrays", modality: "transcription", model: "gpt-transcribe",
			extension: `{"keywords":["Scope","OpenAI"],"languages":["en","zh"]}`,
			fields:    map[string][]string{"keywords[]": {"Scope", "OpenAI"}, "languages[]": {"en", "zh"}},
		},
		{
			name: "transcription automatic chunks", modality: "transcription", model: "gpt-4o-transcribe-diarize",
			extension: `{"chunking_strategy":"auto","response_format":"diarized_json"}`,
			fields:    map[string][]string{"chunking_strategy": {"auto"}, "response_format": {"diarized_json"}},
		},
		{
			name: "transcription explicit chunks", modality: "transcription", model: "gpt-4o-transcribe-diarize",
			extension: `{"chunking_strategy":{"type":"server_vad","prefix_padding_ms":0,"silence_duration_ms":2000,"threshold":0.5},"known_speaker_names":["agent"],"known_speaker_references":["data:audio/wav;base64,YXVkaW8="],"response_format":"diarized_json"}`,
			fields:    map[string][]string{"chunking_strategy[type]": {"server_vad"}, "chunking_strategy[prefix_padding_ms]": {"0"}, "chunking_strategy[silence_duration_ms]": {"2000"}, "chunking_strategy[threshold]": {"0.5"}, "known_speaker_names[]": {"agent"}, "known_speaker_references[]": {"data:audio/wav;base64,YXVkaW8="}, "response_format": {"diarized_json"}},
		},
		{
			name: "translation zero temperature", modality: "translation", model: "whisper-1",
			extension: `{"temperature":0,"prompt":"English names","response_format":"json"}`,
			fields:    map[string][]string{"temperature": {"0"}, "prompt": {"English names"}, "response_format": {"json"}},
		},
		{
			name: "translation fractional temperature", modality: "translation", model: "whisper-1",
			extension: `{"temperature":0.25,"response_format":"verbose_json"}`,
			fields:    map[string][]string{"temperature": {"0.25"}, "response_format": {"verbose_json"}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			observations := make(chan map[string][]string, 1)
			server := modeltest.JSONServer(http.StatusOK, `{"text":"transcribed"}`, func(wire *http.Request) {
				if err := wire.ParseMultipartForm(1 << 20); err != nil {
					t.Error(err)
					return
				}
				defer wire.MultipartForm.RemoveAll()
				select {
				case observations <- wire.MultipartForm.Value:
				default:
					t.Error("unexpected additional audio request")
				}
			})
			t.Cleanup(server.Close)
			model := newAudioRequestModel(t, test.modality, test.model, server.URL)
			request := newAudioExtensionRequest(t, test.modality, json.RawMessage(test.extension))
			request.Options.Language = test.language
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.Output.Text != "transcribed" {
				t.Fatalf("text = %q", response.Output.Text)
			}
			test.fields["model"] = []string{test.model}
			if test.language != "" {
				test.fields["language"] = []string{test.language}
			}
			select {
			case observed := <-observations:
				if !reflect.DeepEqual(observed, test.fields) {
					t.Fatalf("multipart fields = %#v; want %#v", observed, test.fields)
				}
			default:
				t.Fatal("no audio request observed")
			}
		})
	}
}

func TestAudioRequestExtensionsRejectUnrepresentableFieldsBeforeIO(t *testing.T) {
	for _, modality := range []string{"transcription", "translation"} {
		for _, raw := range []string{
			`{"stream":true}`, `{"stream":false}`, `{"stream":"true"}`, `{"stream":null}`,
			`{"response_format":"text"}`, `{"response_format":"srt"}`, `{"response_format":"vtt"}`,
			`{"response_format":"future"}`, `{"response_format":""}`, `{"response_format":null}`,
			`{"response_format":false}`, `{"temperature":"0.5"}`, `{"temperature":false}`, `{"temperature":null}`, `{"temperature":1e1000}`,
			`{"future_field":"value"}`, `{"model":null}`, `{"file":null}`, `{"language":null}`,
			`{"paramObj":{"any":"hidden"}}`, `{"metadata":{}}`, `{"Temperature":0.5}`,
			`null`, `[]`, `"fields"`,
		} {
			t.Run(modality+"/"+raw, func(t *testing.T) {
				assertAudioExtensionRejected(t, modality, raw)
			})
		}
	}
	for _, raw := range []string{
		`{"include":[]}`, `{"include":[null]}`, `{"timestamp_granularities":[]}`,
		`{"languages":[]}`, `{"known_speaker_names":[]}`, `{"known_speaker_references":[]}`, `{"keywords":[]}`,
		`{"chunking_strategy":null}`, `{"chunking_strategy":true}`, `{"chunking_strategy":"future"}`,
		`{"chunking_strategy":{}}`, `{"chunking_strategy":{"type":"future"}}`,
		`{"chunking_strategy":{"type":"server_vad","future_field":1}}`,
		`{"chunking_strategy":{"type":"server_vad","prefix_padding_ms":1.5}}`,
		`{"chunking_strategy":{"type":"server_vad","threshold":null}}`,
	} {
		t.Run("transcription/"+raw, func(t *testing.T) {
			assertAudioExtensionRejected(t, "transcription", raw)
		})
	}
	t.Run("translation/diarized_json", func(t *testing.T) {
		assertAudioExtensionRejected(t, "translation", `{"response_format":"diarized_json"}`)
	})
}

func TestAudioRequestTypeErrorPreservesSDKCause(t *testing.T) {
	err := assertAudioExtensionRejected(t, "transcription", `{"temperature":"warm"}`)
	if cause, found := errors.AsType[*jsonv2.SemanticError](err); !found || cause.Err == nil {
		t.Fatalf("SDK type error was lost: %v", err)
	}
}

func assertAudioExtensionRejected(t *testing.T, modality, raw string) error {
	t.Helper()
	var requests modeltest.PollCounter
	server := modeltest.JSONServer(http.StatusOK, `{"text":"unexpected network request"}`, func(*http.Request) { requests.Inc() })
	t.Cleanup(server.Close)
	model := newAudioRequestModel(t, modality, "whisper-1", server.URL)
	request := newAudioExtensionRequest(t, modality, json.RawMessage(raw))
	response, err := model.Call(t.Context(), request)
	if err == nil || response != nil || requests.N() != 0 {
		t.Fatalf("response = %v; error = %v; requests = %d", response, err, requests.N())
	}
	if !strings.Contains(err.Error(), "extension") {
		t.Fatalf("error does not identify the extension: %v", err)
	}
	return err
}

func newAudioRequestModel(t *testing.T, modality, modelID, baseURL string) transcription.Model {
	t.Helper()
	defaults := transcription.Options{Model: modelID}
	var model transcription.Model
	var err error
	switch modality {
	case "transcription":
		model, err = openai.NewTranscriptionModel(t.Context(), openai.TranscriptionModelConfig{Provider: "openai", APIKey: "test-key", DefaultOptions: defaults, BaseURL: baseURL})
	case "translation":
		model, err = openai.NewAudioTranslationModel(t.Context(), openai.AudioTranslationModelConfig{Provider: "openai", APIKey: "test-key", DefaultOptions: defaults, BaseURL: baseURL})
	default:
		t.Fatalf("unknown audio modality %q", modality)
	}
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func newAudioExtensionRequest(t *testing.T, modality string, extension any) *transcription.Request {
	t.Helper()
	var extensionKey string
	switch modality {
	case "transcription":
		extensionKey = openai.TranscriptionRequestExtensionKey
	case "translation":
		extensionKey = openai.TranslationRequestExtensionKey
	default:
		t.Fatalf("unknown audio modality %q", modality)
	}
	audio, err := media.NewBytes("audio/wav", []byte("offline audio"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := transcription.NewRequest(audio)
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Options.Extensions.Set(extensionKey, extension); err != nil {
		t.Fatal(err)
	}
	return request
}
