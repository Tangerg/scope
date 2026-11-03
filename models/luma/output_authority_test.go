package luma_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/models/luma"
)

type assetAuthorityTransport struct {
	outputURL   string
	downloadErr error
	redirect    string
	downloads   []*http.Request
}

func (a *assetAuthorityTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	header := http.Header{"Content-Type": {"application/json"}}
	status := http.StatusOK
	var body string
	switch {
	case request.Method == http.MethodPost:
		body = `{"id":"generation","state":"queued"}`
	case strings.Contains(request.URL.Path, "/generations/"):
		body = fmt.Sprintf(`{"id":"generation","state":"completed","created_at":"2026-10-26T00:00:00Z","output":[{"type":"image","url":%q}]}`, a.outputURL)
	default:
		a.downloads = append(a.downloads, request.Clone(request.Context()))
		if a.downloadErr != nil {
			return nil, a.downloadErr
		}
		header.Set("Content-Type", "image/png")
		body = "PNG"
		if a.redirect != "" && request.URL.Path == "/start" {
			status = http.StatusTemporaryRedirect
			header.Set("Location", a.redirect)
		}
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestOutputOriginAuthorityIsExplicitAndImmutable(t *testing.T) {
	for _, output := range []string{"https://assets.example/image", "https://other.example/image", "https://assets.example:9443/image", "http://assets.example/image"} {
		t.Run(output, func(t *testing.T) {
			transport := &assetAuthorityTransport{outputURL: output}
			jar, err := cookiejar.New(nil)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := url.Parse(output)
			if err != nil {
				t.Fatal(err)
			}
			jar.SetCookies(endpoint, []*http.Cookie{{Name: "session", Value: "host-secret"}})
			client := &http.Client{Transport: transport, Jar: jar}
			origins := []string{"https://assets.example"}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "provider-secret", BaseURL: "https://api.example/v1", HTTPClient: client, OutputOrigins: origins, DefaultOptions: image.Options{Model: luma.ModelUni1}})
			if err != nil {
				t.Fatal(err)
			}
			origins[0] = "https://other.example"
			response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
			if output == "https://assets.example/image" {
				if err != nil || response == nil || string(response.First().Media.Source.Bytes) != "PNG" || len(transport.downloads) != 1 {
					t.Fatalf("response=%v error=%v downloads=%d", response, err, len(transport.downloads))
				}
				for _, header := range []string{"Cookie", "Authorization", "Proxy-Authorization"} {
					if got := transport.downloads[0].Header.Get(header); got != "" {
						t.Errorf("output received %s=%q", header, got)
					}
				}
			} else if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || len(transport.downloads) != 0 {
				t.Fatalf("unauthorized output: response=%v error=%v downloads=%d", response, err, len(transport.downloads))
			}
			if client.Jar != jar || client.Transport != transport || client.CheckRedirect != nil {
				t.Fatal("borrowed HTTP client mutated")
			}
		})
	}
}

func TestOutputRedirectCannotExpandAuthorityOrCarryCredentials(t *testing.T) {
	for _, change := range []struct {
		name        string
		destination string
		host        string
		allowed     bool
	}{
		{"allowed origin", "https://assets.example/final", "", true},
		{"other origin", "https://other.example/final", "", false},
		{"virtual authority", "https://assets.example/final", "other.example", false},
		{"HTTPS downgrade", "http://assets.example/final", "", false},
	} {
		t.Run(change.name, func(t *testing.T) {
			transport := &assetAuthorityTransport{outputURL: "https://assets.example/start", redirect: change.destination}
			client := &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				request.Header.Set("Authorization", "Bearer callback-secret")
				request.Header.Set("Cookie", "session=callback-secret")
				request.Host = change.host
				return nil
			}}
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "provider-secret", BaseURL: "https://api.example/v1", HTTPClient: client, OutputOrigins: []string{"https://assets.example", "http://assets.example"}, DefaultOptions: image.Options{Model: luma.ModelUni1}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
			if change.allowed {
				if err != nil || response == nil || len(transport.downloads) != 2 {
					t.Fatalf("response=%v error=%v downloads=%d", response, err, len(transport.downloads))
				}
				for _, request := range transport.downloads {
					if request.Header.Get("Authorization") != "" || request.Header.Get("Cookie") != "" {
						t.Fatalf("output credentials=%v", request.Header)
					}
				}
			} else if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || len(transport.downloads) != 1 {
				t.Fatalf("redirect escaped output authority: response=%v error=%v downloads=%d", response, err, len(transport.downloads))
			}
		})
	}
}

func TestOutputOriginsAreValidatedBeforeInvocation(t *testing.T) {
	for _, origins := range [][]string{nil, {""}, {"ftp://assets.example"}, {"https://user:pass@assets.example"}, {"https://assets.example/path"}, {"https://assets.example?key=value"}} {
		model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "key", OutputOrigins: origins, DefaultOptions: image.Options{Model: luma.ModelUni1}})
		if err == nil || model != nil {
			t.Fatalf("origins=%q model=%v error=%v", origins, model, err)
		}
	}
}

func TestOutputAdmissionCannotEraseCompletedGeneration(t *testing.T) {
	cause := errors.New("output transport refused before sending")
	transport := &assetAuthorityTransport{outputURL: "https://assets.example/image", downloadErr: &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: cause}}
	model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "key", BaseURL: "https://api.example/v1", HTTPClient: &http.Client{Transport: transport}, OutputOrigins: []string{"https://assets.example"}, DefaultOptions: image.Options{Model: luma.ModelUni1}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
	if response != nil || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, cause) || len(transport.downloads) != 1 {
		t.Fatalf("response=%v error=%v downloads=%d", response, err, len(transport.downloads))
	}
}
