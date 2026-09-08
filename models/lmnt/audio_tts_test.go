package lmnt_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/lmnt"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	var requestBody struct {
		Text  string `json:"text"`
		Voice string `json:"voice"`
		Model string `json:"model"`
	}
	srv := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("X-API-Key") != "test-key" {
			t.Errorf("X-API-Key = %q", request.Header.Get("X-API-Key"))
		}
		if request.Header.Get("lmnt-version") != lmnt.CurrentAPIVersion {
			t.Errorf("lmnt-version = %q", request.Header.Get("lmnt-version"))
		}
		if err := json.NewDecoder(request.Body).Decode(&requestBody); err != nil {
			t.Errorf("decode request: %v", err)
			http.Error(writer, "bad request", http.StatusBadRequest)
			return
		}
		writer.Header().Set("Content-Type", "audio/wav")
		writer.Header().Set("request-id", "request-1")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("FAKE-WAV"))
	}))
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: lmnt.ModelBlizzard}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "lily"
	m, err := lmnt.NewAudioTTSModel(t.Context(), lmnt.AudioTTSModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := tts.NewRequest("hello world")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil {
		t.Fatal("nil output")
	}
	if string(out.Output.Audio) != "FAKE-WAV" {
		t.Fatalf("audio = %q", out.Output.Audio)
	}
	if requestBody.Text != "hello world" || requestBody.Model != lmnt.ModelBlizzard || requestBody.Voice != "lily" {
		t.Fatalf("request = %#v", requestBody)
	}

	limited, err := lmnt.NewAudioTTSModel(t.Context(), lmnt.AudioTTSModelConfig{
		APIKey:           "test-key",
		DefaultOptions:   opts,
		BaseURL:          srv.URL,
		MaxResponseBytes: 4,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limited.Call(t.Context(), req); err == nil || !strings.Contains(err.Error(), "4-byte limit") {
		t.Fatalf("limited Call error = %v", err)
	}
}

func TestAudioTTSModelConfigRejectsNegativeResponseLimit(t *testing.T) {
	opts := tts.Options{Model: lmnt.ModelBlizzard}
	opts.Voice = "lily"
	_, err := lmnt.NewAudioTTSModel(t.Context(), lmnt.AudioTTSModelConfig{
		APIKey: "test-key", DefaultOptions: opts, MaxResponseBytes: -1,
	})
	if err == nil {
		t.Fatal("NewAudioTTSModel accepted a negative response limit")
	}
}
