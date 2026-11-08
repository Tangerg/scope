package protocol

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"
)

type generationPolicyTransport func(*http.Request) (*http.Response, error)

func (g generationPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	return g(request)
}

type generationPolicyJar struct{ reads, writes int }

func (g *generationPolicyJar) Cookies(*url.URL) []*http.Cookie {
	g.reads++
	return []*http.Cookie{{Name: "session", Value: "host"}}
}

func (g *generationPolicyJar) SetCookies(*url.URL, []*http.Cookie) { g.writes++ }

type generationPolicyBody struct {
	io.Reader
	closed int
}

func (g *generationPolicyBody) Close() error {
	g.closed++
	return nil
}

type generationPolicyContextKey struct{}

func TestGenerationPreparationPreservesHTTPPolicyAndOwnership(t *testing.T) {
	for _, backend := range []genai.Backend{genai.BackendGeminiAPI, genai.BackendVertexAI} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("backend=%d/stream=%t", backend, streaming), func(t *testing.T) {
				responseJSON := `{"candidates":[{"content":{"parts":[{"text":"done"}]},"finishReason":"STOP"}]}`
				if streaming {
					responseJSON = "data: " + responseJSON + "\n\n"
				}
				body := &generationPolicyBody{Reader: strings.NewReader(responseJSON)}
				jar := &generationPolicyJar{}
				calls := 0
				transport := generationPolicyTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					if request.Context().Err() != nil || request.Context().Value(generationPolicyContextKey{}) != "invocation" {
						t.Errorf("real request lost its live context: %v", request.Context().Err())
					}
					if _, found := request.Context().Deadline(); !found {
						t.Error("request timeout did not reach real HTTP")
					}
					if request.Header.Get("X-Request") != "caller" || request.Header.Get("Cookie") != "session=host" || request.Header.Get("X-Server-Timeout") == "" {
						t.Errorf("headers=%v", request.Header)
					}
					if backend == genai.BackendGeminiAPI && request.Header.Get(headerGoogleAPIKey) != "bound-key" {
						t.Error("request lost the bound API key")
					}
					raw, err := io.ReadAll(request.Body)
					if err != nil {
						t.Fatal(err)
					}
					replay, err := request.GetBody()
					if err != nil {
						t.Fatal(err)
					}
					replayed, err := io.ReadAll(replay)
					replay.Close()
					if err != nil || !bytes.Equal(raw, replayed) || request.ContentLength != int64(len(raw)) {
						t.Errorf("request body ownership failed: error=%v length=%d/%d", err, request.ContentLength, len(raw))
					}
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Set-Cookie": {"session=next"}}, Body: body, Request: request}, nil
				})
				host := &http.Client{Transport: transport, Jar: jar, Timeout: time.Second}
				config := ClientConfig{Backend: backend, APIKey: "bound-key", BaseURL: "https://generation.example.test", HTTPClient: host}
				if backend == genai.BackendVertexAI {
					config.Project, config.Location, config.APIKey = "test", "global", ""
				}
				client, err := NewGenerationClient(t.Context(), config)
				if err != nil {
					t.Fatal(err)
				}
				options := &genai.GenerateContentConfig{
					SystemInstruction: &genai.Content{Parts: []*genai.Part{{Text: "system"}}},
					HTTPOptions:       &genai.HTTPOptions{Headers: http.Header{"x-request": {"caller"}}, Timeout: new(time.Second)},
				}
				originalHeaders := options.HTTPOptions.Headers.Clone()
				ctx := context.WithValue(t.Context(), generationPolicyContextKey{}, "invocation")
				contents := []*genai.Content{genai.NewContentFromText("hello", genai.RoleUser)}
				if streaming {
					for _, err := range client.GenerateContentStream(ctx, "gemini-test", contents, options) {
						if err != nil {
							t.Fatal(err)
						}
					}
				} else if _, err := client.GenerateContent(ctx, "gemini-test", contents, options); err != nil {
					t.Fatal(err)
				}
				if calls != 1 || jar.reads != 1 || jar.writes != 1 || body.closed != 1 {
					t.Fatalf("real HTTP=%d jar=%d/%d body closes=%d", calls, jar.reads, jar.writes, body.closed)
				}
				if options.SystemInstruction.Role != "" || options.AutomaticContinuation != nil || options.HTTPOptions.ExtrasRequestProvider != nil || !reflect.DeepEqual(options.HTTPOptions.Headers, originalHeaders) {
					t.Fatalf("SDK preparation changed caller options: %#v", options)
				}
			})
		}
	}
}
