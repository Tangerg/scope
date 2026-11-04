package ollama_test

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/ollama"
)

func TestStreamBindsFirstReportedModelIndependentlyOfRequestAlias(t *testing.T) {
	for _, test := range []struct {
		name, first, last, want string
		invalid                 bool
	}{
		{name: "resolved alias", first: "actual-model", last: "actual-model", want: "actual-model"},
		{name: "late model", last: "actual-model", want: "actual-model"},
		{name: "later omission", first: "actual-model", want: "actual-model"},
		{name: "unreported model", want: "request-alias"},
		{name: "changed model", first: "model-a", last: "model-b", invalid: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set("Content-Type", "application/x-ndjson")
				fmt.Fprintf(writer, "{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":\"first\"},\"done\":false}\n", test.first)
				fmt.Fprintf(writer, "{\"model\":%q,\"message\":{\"role\":\"assistant\",\"content\":\"second\"},\"done\":true,\"done_reason\":\"stop\"}\n", test.last)
			}))
			t.Cleanup(server.Close)
			model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
				BaseURL: server.URL, DefaultOptions: chat.Options{Model: "request-alias"},
			})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			response, err := model.Call(t.Context(), request)
			if !test.invalid {
				if err != nil || response == nil || response.Text() != "firstsecond" || response.Metadata.Model != test.want {
					t.Fatalf("response=%#v error=%v; want firstsecond from %q", response, err, test.want)
				}
				return
			}
			if response != nil || !errors.Is(err, chat.ErrInvalidResponse) {
				t.Fatalf("mixed-model Call response=%#v error=%v", response, err)
			}
			var text string
			failures, terminals := 0, 0
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					if !errors.Is(err, chat.ErrInvalidResponse) || delta != nil {
						t.Fatalf("stream failure=%v delta=%#v", err, delta)
					}
					failures++
					continue
				}
				if delta.FinishReason != "" {
					terminals++
				}
				for _, part := range delta.Parts {
					if part.Kind == chat.PartDeltaText {
						text += part.Text
					}
				}
			}
			if text != "first" || failures != 1 || terminals != 0 {
				t.Fatalf("stream text=%q failures=%d terminals=%d", text, failures, terminals)
			}
		})
	}
}
