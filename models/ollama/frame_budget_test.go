package ollama_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/ollama"
)

type frameBody struct {
	io.Reader
	read   int
	closed bool
}

func (f *frameBody) Read(buffer []byte) (int, error) {
	n, err := f.Reader.Read(buffer)
	f.read += n
	return n, err
}
func (f *frameBody) Close() error { f.closed = true; return nil }

type frameTransport struct{ body *frameBody }

func (f frameTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": []string{"application/x-ndjson"}}, Body: f.body}, nil
}

func TestNativeFrameBudgetPrecedesDecoding(t *testing.T) {
	const limit = 8 << 20
	for _, closed := range []bool{false, true} {
		raw := `{"model":"model","message":{"role":"assistant","content":"` + strings.Repeat("x", limit+(1<<20))
		if closed {
			raw += `"},"done":true,"done_reason":"stop"}`
		}
		body := &frameBody{Reader: strings.NewReader(raw)}
		model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{DefaultOptions: chat.Options{Model: "model"}, HTTPClient: &http.Client{Transport: frameTransport{body}}})
		if err != nil {
			t.Fatal(err)
		}
		request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("text")))
		failures := 0
		for delta, err := range model.Stream(t.Context(), request) {
			if delta != nil || err == nil {
				t.Fatalf("oversized frame produced %v, %v", delta, err)
			}
			failures++
		}
		if failures != 1 || body.read > limit+2 || !body.closed {
			t.Fatalf("frame admission: errors=%d read=%d closed=%t", failures, body.read, body.closed)
		}
	}
}
