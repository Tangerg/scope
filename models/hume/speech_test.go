package hume_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

func TestSpeechPreservesNativeContextIntegerPrecision(t *testing.T) {
	audioB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-MP3"))
	body := `{"generations":[{"generation_id":"g1","audio":"` + audioB64 + `","duration":1.0,"file_size":8,"snippets":[],"encoding":{"format":"mp3","sample_rate":44100}}]}`
	var captured string
	srv := modeltest.JSONServer(http.StatusOK, body, func(r *http.Request) {
		if raw, err := io.ReadAll(r.Body); err == nil {
			captured = string(raw)
		}
	})
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: hume.ModelOctave2, Voice: "test-voice"}
	if err := opts.Extensions.Set(hume.SpeechRequestExtensionKey, json.RawMessage(`{"context":{"external_id":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	m, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test-key", DefaultOptions: opts, BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	req, _ := speech.NewRequest("hello")
	if _, err := m.Call(t.Context(), req); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(captured, "9007199254740993") || strings.Contains(captured, "9007199254740992") {
		t.Fatalf("serialized request lost integer precision: %s", captured)
	}
}

func TestSpeechModel_Call_Mock(t *testing.T) {
	audioB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-MP3"))
	body := `{"generations":[{"generation_id":"g1","audio":"` + audioB64 + `","duration":1.0,"file_size":8,"snippets":[],"encoding":{"format":"mp3","sample_rate":44100}}]}`
	srv := modeltest.JSONServer(http.StatusOK, body, func(r *http.Request) {
		if r.URL.Path != "/tts" {
			t.Errorf("path = %q", r.URL.Path)
		}
	})
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: hume.ModelOctave2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "test-voice"
	m, err := hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := speech.NewRequest("hello world")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.Output == nil {
		t.Fatal("nil output")
	}
}

func TestSpeechModel_Stream_Mock(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte("first"))
	second := base64.StdEncoding.EncodeToString([]byte("second"))
	srv := modeltest.MuxServer(modeltest.Route{Method: "POST", Contains: "/tts/stream/json", Handle: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"type":"audio","audio":"` + first + `","audio_format":"mp3","chunk_index":0,"generation_id":"g1","request_id":"r1","snippet_id":"s1","text":"hello","is_last_chunk":false}` + "\n"))
		w.Write([]byte(`{"type":"audio","audio":"` + second + `","audio_format":"mp3","chunk_index":1,"generation_id":"g1","request_id":"r1","snippet_id":"s1","text":"hello","is_last_chunk":true}` + "\n"))
	}})
	t.Cleanup(srv.Close)

	opts := speech.Options{Model: hume.ModelOctave2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "test-voice"
	model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := speech.NewRequest("hello")
	var audio strings.Builder
	for response, streamErr := range model.Stream(t.Context(), request) {
		if streamErr != nil {
			t.Fatal(streamErr)
		}
		audio.Write(response.Output.Audio)
	}
	if got := audio.String(); got != "firstsecond" {
		t.Fatalf("streamed audio = %q", got)
	}
}

func TestSpeechCapabilitiesAreSeparate(t *testing.T) {
	if _, ok := any((*hume.SpeechModel)(nil)).(speech.Streamer); ok {
		t.Fatal("unary synthesis exposes streaming")
	}
	if _, ok := any((*hume.StreamingSpeechModel)(nil)).(speech.Model); ok {
		t.Fatal("streaming synthesis exposes independent unary protocol")
	}
}

func TestStreamCompletesEveryStartedSnippet(t *testing.T) {
	first := `{"type":"audio","audio":"UE5H","audio_format":"mp3","chunk_index":0,"generation_id":"g1","request_id":"r1","snippet_id":"s1","is_last_chunk":false}`
	final := strings.Replace(strings.Replace(first, `"chunk_index":0`, `"chunk_index":1`, 1), `"is_last_chunk":false`, `"is_last_chunk":true`, 1)
	second := strings.Replace(strings.Replace(final, `"snippet_id":"s1"`, `"snippet_id":"s2"`, 1), `"chunk_index":1`, `"chunk_index":0`, 1)
	for _, sample := range []struct {
		name, body string
		chunks     int
		fail       bool
	}{
		{"empty", "", 0, true},
		{"truncated", first + "\n", 1, true},
		{"two snippets", first + "\n" + final + "\n" + second + "\n", 3, false},
		{"after final", first + "\n" + final + "\n" + final + "\n", 2, true},
		{"duplicate index", first + "\n" + first + "\n", 1, true},
	} {
		t.Run(sample.name, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, sample.body)
			defer server.Close()
			model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: hume.ModelOctave2, Voice: "voice"}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := speech.NewRequest("text")
			chunks, failures := 0, 0
			for response, err := range model.Stream(t.Context(), request) {
				if err != nil {
					failures++
				} else if response != nil {
					chunks++
				}
			}
			want := 0
			if sample.fail {
				want = 1
			}
			if chunks != sample.chunks || failures != want {
				t.Fatalf("chunks=%d errors=%d", chunks, failures)
			}
		})
	}
}
