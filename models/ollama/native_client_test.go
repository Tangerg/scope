package ollama_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/models/ollama"
)

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
	server := jsonServer(http.StatusServiceUnavailable, `{"error":"daemon warming"}`)
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
	server := jsonServer(http.StatusOK, `{"error":"model is missing"}`)
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
