package ollama_test

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/ollama"
)

func TestNativeChatRejectsUnknownOrNullExtensionBeforeIO(t *testing.T) {
	for _, raw := range []string{`{"thnik":false}`, `null`} {
		t.Run(raw, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				writer.WriteHeader(http.StatusBadRequest)
			}))
			t.Cleanup(server.Close)
			model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "qwen3:8b"}})
			if err != nil {
				t.Fatal(err)
			}
			request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
			if err != nil {
				t.Fatal(err)
			}
			if err := request.Options.Extensions.Set(ollama.RequestExtensionKey, json.RawMessage(raw)); err != nil {
				t.Fatal(err)
			}
			if response, err := model.Call(t.Context(), request); err == nil || response != nil {
				t.Fatalf("Call response = %#v, error = %v", response, err)
			}
			failures := 0
			for delta, err := range model.Stream(t.Context(), request) {
				if delta != nil || err == nil {
					t.Fatalf("Stream delta = %#v, error = %v", delta, err)
				}
				failures++
			}
			if calls.Load() != 0 || failures != 1 {
				t.Fatalf("HTTP calls = %d, terminal errors = %d", calls.Load(), failures)
			}
		})
	}
}

func TestNativeChatPreservesSupportedControlsAndOpenOptions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body struct {
			Think     *bool                      `json:"think"`
			KeepAlive string                     `json:"keep_alive"`
			Options   map[string]json.RawMessage `json:"options"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &body); err != nil {
			t.Error(err)
		}
		if body.Think == nil || *body.Think || body.KeepAlive != "5m0s" || string(body.Options["future_option"]) != "9007199254740993" {
			t.Errorf("native controls = %#v", body)
		}
		writer.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(server.Close)
	model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{BaseURL: server.URL, DefaultOptions: corechat.Options{Model: "qwen3:8b"}})
	if err != nil {
		t.Fatal(err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	if err := request.Options.Extensions.Set(ollama.RequestExtensionKey, json.RawMessage(`{"think":false,"keep_alive":"5m","options":{"future_option":9007199254740993}}`)); err != nil {
		t.Fatal(err)
	}
	if response, err := model.Call(t.Context(), request); err != nil || response.Text() != "ok" {
		t.Fatalf("response = %#v, error = %v", response, err)
	}
}

func TestNativeChatUsesOnlyTheDocumentedLocalHTTPContract(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/api/chat" {
			t.Errorf("path = %q, want /api/chat", request.URL.Path)
		}
		if request.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", request.Header.Get("Content-Type"))
		}
		if request.Header.Get("Accept") != "application/x-ndjson" {
			t.Errorf("Accept = %q", request.Header.Get("Accept"))
		}
		if authorization := request.Header.Get("Authorization"); authorization != "" {
			t.Errorf("unexpected implicit credential %q", authorization)
		}
		writer.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(writer, `{"model":"qwen3:8b","message":{"role":"assistant","content":"ok"},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(server.Close)

	model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "qwen3:8b"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	response, err := model.Call(t.Context(), request)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if response.Text() != "ok" {
		t.Fatalf("response text = %q, want ok", response.Text())
	}
}

func TestNativeChatReportsProviderStatusAndMessage(t *testing.T) {
	server := modeltest.JSONServer(http.StatusServiceUnavailable, `{"error":"daemon warming"}`)
	t.Cleanup(server.Close)
	model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "qwen3:8b"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatalf("NewChat: %v", err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "503 Service Unavailable: daemon warming") {
		t.Fatalf("Call error = %v, want provider status and message", err)
	}
}

func TestNativeChatReportsSuccessfulStatusErrorFrame(t *testing.T) {
	server := modeltest.JSONServer(http.StatusOK, `{"error":"model is missing"}`)
	t.Cleanup(server.Close)
	model, err := ollama.NewChat(t.Context(), ollama.ChatConfig{
		DefaultOptions: corechat.Options{Model: "missing"},
		BaseURL:        server.URL,
	})
	if err != nil {
		t.Fatal(err)
	}
	request, err := corechat.NewRequest(corechat.NewUserMessage(corechat.NewTextPart("hello")))
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Call(t.Context(), request)
	if err == nil || !strings.Contains(err.Error(), "model is missing") {
		t.Fatalf("Call error = %v, want provider frame error", err)
	}
}
