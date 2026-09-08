package hume_test

import (
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/modeltest"
	tts "github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/hume"
)

func TestAudioTTSModel_Call_Mock(t *testing.T) {
	audioB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-MP3"))
	body := `{"generations":[{"generation_id":"g1","audio":"` + audioB64 + `","duration":1.0,"file_size":8,"snippets":[],"encoding":{"format":"mp3","sample_rate":44100}}]}`
	srv := modeltest.JSONServer(http.StatusOK, body, func(r *http.Request) {
		if r.URL.Path != "/tts" {
			t.Errorf("path = %q", r.URL.Path)
		}
	})
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: hume.ModelOctave2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "test-voice"
	m, err := hume.NewAudioTTSModel(t.Context(), hume.AudioTTSModelConfig{
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
}

func TestAudioTTSModel_Stream_Mock(t *testing.T) {
	first := base64.StdEncoding.EncodeToString([]byte("first"))
	second := base64.StdEncoding.EncodeToString([]byte("second"))
	srv := modeltest.MuxServer(modeltest.Route{Method: "POST", Contains: "/tts/stream/json", Handle: func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Write([]byte(`{"type":"audio","audio":"` + first + `","audio_format":"mp3","chunk_index":0,"generation_id":"g1","request_id":"r1","snippet_id":"s1","text":"hello"}` + "\n"))
		w.Write([]byte(`{"type":"audio","audio":"` + second + `","audio_format":"mp3","chunk_index":1,"generation_id":"g1","request_id":"r1","snippet_id":"s1","text":"hello","is_last_chunk":true}` + "\n"))
	}})
	t.Cleanup(srv.Close)

	opts := tts.Options{Model: hume.ModelOctave2}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	opts.Voice = "test-voice"
	model, err := hume.NewAudioTTSModel(t.Context(), hume.AudioTTSModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := tts.NewRequest("hello")
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
