package elevenlabs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/speech"
)

func TestSpeechRejectsEmptySuccessfulAudio(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	model, err := NewSpeechModel(t.Context(), SpeechModelConfig{APIKey: "test", DefaultOptions: speech.Options{Model: "test", Voice: "test"}, BaseURL: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	request, err := speech.NewRequest("hello")
	if err != nil {
		t.Fatal(err)
	}
	events := 0
	for response, err := range model.Stream(t.Context(), request) {
		events++
		if response != nil || !errors.Is(err, speech.ErrInvalidResponse) {
			t.Fatalf("response=%v error=%v", response, err)
		}
	}
	if events != 1 {
		t.Fatalf("events=%d; want one terminal failure", events)
	}
	if response, err := model.Call(t.Context(), request); response != nil || !errors.Is(err, speech.ErrInvalidResponse) {
		t.Fatalf("Call response=%v error=%v", response, err)
	}
}
