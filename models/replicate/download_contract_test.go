package replicate_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/replicate"
)

type observedDownload struct {
	host          string
	authority     string
	authorization string
}

type outputTransport struct {
	outputURL string
	redirect  string
	downloads []observedDownload
}

func (o *outputTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if o == nil {
		return nil, errors.New("typed-nil transport was called")
	}
	if request.Body != nil {
		_ = request.Body.Close()
	}
	header := http.Header{"Content-Type": {"application/json"}}
	status, body := http.StatusOK, `{"id":"prediction","status":"starting"}`
	if request.Method == http.MethodGet && strings.Contains(request.URL.Path, "/predictions/") {
		body = fmt.Sprintf(`{"id":"prediction","status":"succeeded","output":[%q]}`, o.outputURL)
	} else if request.Method == http.MethodGet {
		o.downloads = append(o.downloads, observedDownload{host: request.URL.Host, authority: request.Host, authorization: request.Header.Get("Authorization")})
		header.Set("Content-Type", "image/png")
		body = "PNG"
		if request.URL.Path == "/output" && o.redirect != "" {
			status = http.StatusFound
			header.Set("Location", o.redirect)
		}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func outputModel(t *testing.T, client *http.Client) *replicate.ImageModel {
	t.Helper()
	model, err := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{
		APIKey: "provider-secret", BaseURL: "https://api.example/v1", HTTPClient: client,
		DefaultOptions: image.Options{Model: replicate.ModelFluxSchnell}, InputSchema: replicate.FluxSchnellImageInputSchema(),
	})
	if err != nil {
		t.Fatal(err)
	}
	return model
}

func TestOutputDownloadRejectsHostRewrite(t *testing.T) {
	transport := &outputTransport{outputURL: "https://api.example/output", redirect: "/asset"}
	host := &http.Client{
		Transport: transport,
		CheckRedirect: func(request *http.Request, _ []*http.Request) error {
			request.Host = "other.example"
			return nil
		},
	}
	model := outputModel(t, host)
	response, err := model.Call(t.Context(), &image.Request{Prompt: "a tree"})
	if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || len(transport.downloads) != 1 {
		t.Fatalf("unauthorized authority received output credentials: response=%v error=%v downloads=%+v", response, err, transport.downloads)
	}
}

func TestOutputDownloadUsesDefaultForTypedNilTransport(t *testing.T) {
	transport := &outputTransport{outputURL: "https://api.example/asset"}
	previous := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = previous })
	var typedNil *outputTransport
	host := &http.Client{Transport: typedNil}
	model := outputModel(t, host)
	response, err := model.Call(t.Context(), &image.Request{Prompt: "a tree"})
	if err != nil || response == nil || string(response.First().Media.Source.Bytes) != "PNG" || len(transport.downloads) != 1 {
		t.Fatalf("typed-nil download transport: response=%v error=%v downloads=%+v", response, err, transport.downloads)
	}
	if host.Transport != typedNil || host.CheckRedirect != nil {
		t.Fatal("borrowed HTTP client changed")
	}
}

func TestOutputDownloadRetainsAuthorizedDeliveryOrigins(t *testing.T) {
	for _, outputURL := range []string{"https://api.example/output", "https://a.replicate.delivery/output"} {
		t.Run(outputURL, func(t *testing.T) {
			transport := &outputTransport{outputURL: outputURL, redirect: "https://b.replicate.delivery/asset"}
			model := outputModel(t, &http.Client{Transport: transport})
			response, err := model.Call(t.Context(), &image.Request{Prompt: "a tree"})
			if err != nil || response == nil || string(response.First().Media.Source.Bytes) != "PNG" || len(transport.downloads) != 2 {
				t.Fatalf("authorized delivery: response=%v error=%v downloads=%+v", response, err, transport.downloads)
			}
			for _, download := range transport.downloads {
				if download.host != "api.example" && download.authorization != "" {
					t.Fatalf("delivery received an API credential: %+v", transport.downloads)
				}
			}
		})
	}
}
