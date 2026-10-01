package hume_test

import (
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

func TestSpeechRejectsNativeCoreInputsBeforeIO(t *testing.T) {
	for name, extension := range map[string]any{
		"version":             map[string]any{"version": "1"},
		"text":                map[string]any{"utterances": []any{map[string]any{"text": "other"}}},
		"speed":               map[string]any{"utterances": []any{map[string]any{"speed": 1.2}}},
		"voice id":            map[string]any{"utterances": []any{map[string]any{"voice": map[string]any{"id": "other"}}}},
		"voice name":          map[string]any{"utterances": []any{map[string]any{"voice": map[string]any{"name": "other"}}}},
		"format":              map[string]any{"format": map[string]any{"type": "mp3"}},
		"multiple utterances": map[string]any{"utterances": []any{map[string]any{}, map[string]any{}}},
	} {
		for _, coreOptions := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/core=%t", name, coreOptions), func(t *testing.T) {
				calls := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
				defer server.Close()
				opts := speech.Options{Model: hume.ModelOctave1}
				if coreOptions {
					opts.Voice = "voice-1"
					opts.Speed = 1.1
					opts.OutputFormat = "wav"
				}
				if err := opts.Extensions.Set(hume.SpeechRequestExtensionKey, extension); err != nil {
					t.Fatal(err)
				}
				model, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
				if err != nil {
					t.Fatal(err)
				}
				if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err == nil || calls != 0 {
					t.Fatalf("calls=%d error=%v", calls, err)
				}
			})
		}
	}
}
func TestSpeechPreservesProviderSpecificControls(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Utterances []struct {
				Text        string  `json:"text"`
				Description string  `json:"description"`
				Speed       float64 `json:"speed"`
				Voice       struct {
					ID       string `json:"id"`
					Provider string `json:"provider"`
				} `json:"voice"`
			} `json:"utterances"`
			Format       map[string]any `json:"format"`
			Version      string         `json:"version"`
			StripHeaders *bool          `json:"strip_headers"`
		}
		if err := jsonv2.UnmarshalRead(r.Body, &body); err != nil {
			t.Error(err)
		}
		if len(body.Utterances) != 1 {
			t.Errorf("utterances=%v", body.Utterances)
			return
		}
		value := body.Utterances[0]
		if value.Text != "hello" || value.Description != "calm" || value.Voice.ID != "voice-1" || value.Voice.Provider != "CUSTOM_VOICE" || value.Speed != 1.1 || body.Version != "2" || body.Format["type"] != "wav" || body.Format["sample_rate"] != float64(24000) || body.StripHeaders == nil || *body.StripHeaders {
			t.Errorf("body=%+v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"generations":[{"audio":"YXVkaW8="}]}`)
	}))
	defer server.Close()
	opts := speech.Options{Model: hume.ModelOctave2, Voice: "voice-1", Speed: 1.1, OutputFormat: "wav"}
	if err := opts.Extensions.Set(hume.SpeechRequestExtensionKey, map[string]any{"utterances": []any{map[string]any{"description": "calm", "voice": map[string]any{"provider": "CUSTOM_VOICE"}}}, "format": map[string]any{"sample_rate": 24000}, "strip_headers": false}); err != nil {
		t.Fatal(err)
	}
	model, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}
func TestSpeechRejectsDuplicateResponseMembers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"generations":[],"generations":[{"audio":"YXVkaW8="}]}`)
	}))
	defer server.Close()
	model, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: hume.ModelOctave1}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err == nil {
		t.Fatal("duplicate JSON accepted")
	}
}
