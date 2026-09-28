package modeltest

import (
	"fmt"
	"net/http"
	"net/http/httptest"
)

// OpenAISSEServer frames JSON chunks as data events followed by [DONE].
// The caller owns the server and must close it after use.
func OpenAISSEServer(chunks []string) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", http.StatusInternalServerError)
			return
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
			flusher.Flush()
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		flusher.Flush()
	}))
	return srv
}

type AnthropicEvent struct {
	Event string
	Data  string
}

// AnthropicSSEServer emits named events in the supplied order.
// The caller supplies the complete event sequence and owns server cleanup.
func AnthropicSSEServer(events []AnthropicEvent) *httptest.Server {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", http.StatusInternalServerError)
			return
		}
		for _, e := range events {
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", e.Event, e.Data)
			flusher.Flush()
		}
	}))
	return srv
}

// Inspections run before the response is written and receive the live request.
// Read the body during inspection; it is unavailable after the handler returns.
func JSONServer(status int, body string, inspections ...func(request *http.Request)) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, inspect := range inspections {
			inspect(request)
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.WriteHeader(status)
		fmt.Fprint(writer, body)
	}))
	return server
}

func BinaryServer(status int, contentType string, body []byte, inspections ...func(request *http.Request)) *httptest.Server {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for _, inspect := range inspections {
			inspect(request)
		}
		writer.Header().Set("Content-Type", contentType)
		writer.WriteHeader(status)
		_, _ = writer.Write(body)
	}))
	return server
}
