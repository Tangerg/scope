package moonshot_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/moonshot"
)

func TestNativeRequestHasOneTypedOwner(t *testing.T) {
	for _, defaults := range []bool{false, true} {
		for _, raw := range []string{`{}`, `null`, `{"thinking":{"type":"disabled"}}`} {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; http.Error(w, "unexpected I/O", 500) }))
			options := chat.Options{Model: moonshot.ModelK3}
			request, _ := chat.NewRequest(chat.NewUserMessage(chat.NewTextPart("text")))
			target := &request.Options
			if defaults {
				target = &options
			}
			if err := target.Extensions.Set("moonshot/openai_request", json.RawMessage(raw)); err != nil {
				t.Fatal(err)
			}
			model, err := moonshot.NewChatCompletions(t.Context(), moonshot.ChatCompletionsConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
			if err != nil {
				t.Fatal(err)
			}
			if response, err := model.Call(t.Context(), request); response != nil || err == nil || calls != 0 {
				t.Fatalf("raw field admitted: %v, %v, calls=%d", response, err, calls)
			}
			server.Close()
		}
	}
}
