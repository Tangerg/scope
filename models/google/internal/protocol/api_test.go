package protocol

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestInteractionsRejectsAmbiguousJSON(t *testing.T) {
	for _, direction := range []string{"request", "response"} {
		t.Run(direction, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.Header().Set("Content-Type", "application/json")
				if direction == "response" {
					fmt.Fprint(w, `{"id":"first","id":"second","status":"completed"}`)
					return
				}
				fmt.Fprint(w, `{"id":"interaction","status":"completed"}`)
			}))
			defer server.Close()
			client, err := newAPI(t.Context(), ClientConfig{APIKey: "test", BaseURL: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			request := &imageInteractionRequest{Model: ModelGemini31FlashImage, Input: "a lake"}
			if direction == "request" {
				request.Input = json.RawMessage(`{"text":"first","text":"second"}`)
			}
			response, err := client.createImageInteraction(t.Context(), request)
			if response != nil || !errors.Is(err, jsontext.ErrDuplicateName) {
				t.Fatalf("ambiguous %s accepted: %#v, %v", direction, response, err)
			}
			if direction == "request" && requests.Load() != 0 {
				t.Fatal("invalid JSON reached the transport")
			}
		})
	}
}
