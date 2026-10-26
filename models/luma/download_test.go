package luma_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/luma"
)

type outputTransport struct {
	downloads int
}

func (o *outputTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if o == nil {
		return nil, errors.New("typed-nil transport was called")
	}
	if request.Body != nil {
		_ = request.Body.Close()
	}
	header := http.Header{"Content-Type": {"application/json"}}
	body := `{"id":"generation","state":"queued"}`
	if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/generations/") {
		body = `{"id":"generation","state":"completed","created_at":"2026-10-26T00:00:00Z","output":[{"type":"image","url":"https://asset.example/image"}]}`
	} else if request.Method == http.MethodGet {
		o.downloads++
		header.Set("Content-Type", "image/png")
		body = "PNG"
	}
	return &http.Response{StatusCode: http.StatusOK, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestOutputDownloadSnapshotsBorrowedClientAndResolvesTypedNilTransport(t *testing.T) {
	for _, typedNil := range []bool{false, true} {
		name := "borrowed configuration snapshot"
		if typedNil {
			name = "typed nil uses default"
		}
		t.Run(name, func(t *testing.T) {
			transport := new(outputTransport)
			previous := http.DefaultTransport
			http.DefaultTransport = transport
			t.Cleanup(func() { http.DefaultTransport = previous })
			var absent *outputTransport
			host := &http.Client{Transport: transport}
			if typedNil {
				host.Transport = absent
			}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
				APIKey: "test", BaseURL: "https://api.example/v1", HTTPClient: host,
				DefaultOptions: image.Options{Model: luma.ModelUni1},
			})
			if err != nil {
				t.Fatal(err)
			}
			if !typedNil {
				host.Transport = absent
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "a tree"})
			if err != nil || response == nil || string(response.First().Media.Source.Bytes) != "PNG" || transport.downloads != 1 {
				t.Fatalf("response=%v error=%v downloads=%d", response, err, transport.downloads)
			}
			if host.Transport != absent || host.CheckRedirect != nil {
				t.Fatal("borrowed client configuration changed")
			}
		})
	}
}
