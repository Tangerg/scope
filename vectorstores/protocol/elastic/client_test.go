package elastic

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type failingBody struct {
	io.Reader
	cause  error
	closed bool
}

func (f *failingBody) Close() error { f.closed = true; return f.cause }

type bodyClient struct{ body *failingBody }

func (b bodyClient) Perform(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Body: b.body}, nil
}

func TestNativeBodyOwnershipAndBounds(t *testing.T) {
	cause := errors.New("close failed")
	body := &failingBody{Reader: strings.NewReader(`{}`), cause: cause}
	client, err := NewClient(ClientConfig{Perform: bodyClient{body: body}.Perform, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if _, readErr := client.Read(t.Context(), http.MethodGet, "/documents/_mapping", nil, nil, 0); !errors.Is(readErr, cause) || !body.closed {
		t.Fatalf("close failure disappeared: %v", readErr)
	}
	body = &failingBody{Reader: strings.NewReader(strings.Repeat("x", 17))}
	client, err = NewClient(ClientConfig{Perform: bodyClient{body: body}.Perform, MaxResponseBytes: 16})
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := client.Read(t.Context(), http.MethodGet, "/documents/_mapping", nil, nil, 0); err == nil || len(raw) != 17 || !body.closed {
		t.Fatalf("response bound failed: %s, %v", raw, err)
	}
}

func TestClientRejectsInvalidAdmissionAndUnownedBodies(t *testing.T) {
	for _, config := range []ClientConfig{{}, {Perform: func(*http.Request) (*http.Response, error) { return nil, nil }, MaxResponseBytes: -1}} {
		if client, err := NewClient(config); client != nil || err == nil {
			t.Fatalf("invalid client=%v error=%v", client, err)
		}
	}
	for _, client := range []*Client{nil, {}} {
		if raw, err := client.Read(t.Context(), http.MethodGet, "/", nil, nil, 0); raw != nil || err == nil {
			t.Fatalf("invalid reader=%q error=%v", raw, err)
		}
	}
	var nilBody *failingBody
	for _, response := range []*http.Response{nil, {StatusCode: 200}, {StatusCode: 200, Body: nilBody}} {
		client, err := NewClient(ClientConfig{Perform: func(*http.Request) (*http.Response, error) { return response, nil }})
		if err != nil {
			t.Fatal(err)
		}
		if raw, err := client.Read(t.Context(), http.MethodGet, "/", nil, nil, 0); raw != nil || err == nil {
			t.Fatalf("unowned body=%q error=%v", raw, err)
		}
	}
	transport := &scriptedTransport{t: t}
	client := protocolClient(t, transport, 0)
	if raw, err := client.Read(t.Context(), http.MethodGet, "/", nil, nil, -1); raw != nil || err == nil || len(transport.requests) != 0 {
		t.Fatal("invalid budget reached transport")
	}
	if raw, err := client.Read(t.Context(), "invalid method", "/", nil, nil, 0); raw != nil || err == nil || len(transport.requests) != 0 {
		t.Fatal("invalid request reached transport")
	}
}
