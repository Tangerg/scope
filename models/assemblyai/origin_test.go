package assemblyai_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/assemblyai"
)

type redirectTransport struct {
	origins     []string
	credentials []string
}

func (r *redirectTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	r.origins = append(r.origins, request.URL.Host)
	r.credentials = append(r.credentials, request.Header.Get("Authorization"))
	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	status := http.StatusBadRequest
	if request.URL.Host == "api.example" {
		header.Set("Location", "https://api.example:9443/redirected")
		status = http.StatusTemporaryRedirect
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(`{"error":"unexpected origin"}`)), Request: request}, nil
}

func TestModelCallKeepsCredentialsAndBodyAtAPIOrigin(t *testing.T) {
	const baseURL = "https://api.example/v1"
	transport := new(redirectTransport)
	host := &http.Client{Transport: transport}
	model, err := assemblyai.NewTranscriptionModel(t.Context(), assemblyai.TranscriptionModelConfig{APIKey: "provider-secret", BaseURL: baseURL, HTTPClient: host, DefaultOptions: transcription.Options{Model: assemblyai.ModelUniversal3Point5Pro}})
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewBytes("audio/wav", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	request, err := transcription.NewRequest(audio)
	if err != nil {
		t.Fatal(err)
	}
	_, err = model.Call(t.Context(), request)
	if !errors.Is(err, httpclient.ErrOriginNotAllowed) {
		t.Fatalf("redirect error = %v", err)
	}
	if len(transport.origins) == 0 {
		t.Fatal("the provider request never reached its configured origin")
	}
	for index, origin := range transport.origins {
		if origin != "api.example" || !strings.Contains(transport.credentials[index], "provider-secret") {
			t.Fatalf("credentials or body escaped API origin: origins=%v", transport.origins)
		}
	}
	if host.Transport != transport || host.CheckRedirect != nil {
		t.Fatal("borrowed client configuration was changed")
	}
}
