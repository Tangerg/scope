package deepgram_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/deepgram"
)

func TestOfficialTranscriptionOptionsReachQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RawQuery; got != "diarize=true&keyterm=Scope&keyterm=Go&model=nova-3&punctuate=false&redact=pci&redact=ssn&smart_format=true" {
			t.Errorf("query=%q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"results":{"channels":[{"alternatives":[{"transcript":"redacted speech"}]}]}}`)
	}))
	defer server.Close()
	opts := transcription.Options{Model: "nova-3"}
	if err := opts.Extensions.Set(deepgram.TranscriptionRequestExtensionKey, map[string]any{"redact": []string{"pci", "ssn"}, "smart_format": true, "diarize": true, "punctuate": false, "keyterm": []string{"Scope", "Go"}}); err != nil {
		t.Fatal(err)
	}
	model, err := deepgram.NewTranscriptionModel(t.Context(), deepgram.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &transcription.Request{Audio: audio}); err != nil {
		t.Fatal(err)
	}
}

func TestOfficialSpeechOptionsReachQuery(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.RawQuery; got != "bit_rate=32000&encoding=mp3&model=aura-2-thalia-en&sample_rate=24000&speed=1.1" {
			t.Errorf("query=%q", got)
		}
		w.Header().Set("Content-Type", "audio/mpeg")
		fmt.Fprint(w, "audio")
	}))
	defer server.Close()
	opts := speech.Options{Model: "aura-2-thalia-en", OutputFormat: "mp3", Speed: 1.1}
	if err := opts.Extensions.Set(deepgram.SpeechRequestExtensionKey, map[string]any{"sample_rate": 24000, "bit_rate": 32000}); err != nil {
		t.Fatal(err)
	}
	model, err := deepgram.NewSpeechModel(t.Context(), deepgram.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err != nil {
		t.Fatal(err)
	}
}

func TestExtraCannotOverrideBoundQueryFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("invalid request reached transport")
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()
	for _, field := range []string{"model", "language", "redact", "smart_format"} {
		t.Run(field, func(t *testing.T) {
			opts := transcription.Options{Model: "nova-3"}
			if err := opts.Extensions.Set(deepgram.TranscriptionRequestExtensionKey, map[string]any{"extra": map[string][]string{field: {"other"}}}); err != nil {
				t.Fatal(err)
			}
			model, err := deepgram.NewTranscriptionModel(t.Context(), deepgram.TranscriptionModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
			if err != nil {
				t.Fatal(err)
			}
			audio, err := media.NewBytes("audio/mpeg", []byte("audio"))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &transcription.Request{Audio: audio}); err == nil {
				t.Fatal("Extra field accepted")
			}
		})
	}
	for _, field := range []string{"model", "encoding", "container", "speed", "text", "sample_rate"} {
		t.Run("speech/"+field, func(t *testing.T) {
			opts := speech.Options{Model: "aura-2-thalia-en"}
			if err := opts.Extensions.Set(deepgram.SpeechRequestExtensionKey, map[string]any{"extra": map[string][]string{field: {"other"}}}); err != nil {
				t.Fatal(err)
			}
			model, err := deepgram.NewSpeechModel(t.Context(), deepgram.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: opts})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &speech.Request{Text: "hello"}); err == nil {
				t.Fatal("Extra field accepted")
			}
		})
	}
}
