package web_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/brave"
)

type providerFailureBody struct {
	cause      error
	beforeRead func()
}

func (p *providerFailureBody) Read([]byte) (int, error) {
	if p.beforeRead != nil {
		p.beforeRead()
	}
	return 0, p.cause
}

func (*providerFailureBody) Close() error { return nil }

func prepareProviderTool(t *testing.T, executable tool.Tool, arguments string) (tool.Binding, tool.Invocation) {
	t.Helper()
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(tool.Request{Name: executable.Definition().Name, Arguments: arguments}, tool.Admission{})
	if err != nil {
		t.Fatal(err)
	}
	return binding, invocation
}

func providerDiagnostic(t *testing.T, err error) error {
	t.Helper()
	diagnostic, ok := errors.AsType[interface {
		error
		Cause() error
	}](err)
	if !ok || diagnostic.Cause() == nil {
		t.Fatalf("missing explicit provider diagnostic: %v", err)
	}
	return diagnostic.Cause()
}

func TestProviderIOErrorsCannotOwnToolOutcomes(t *testing.T) {
	foreign, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New("foreign diagnostic"), Output: tool.NewTextOutput("foreign model feedback"),
	})
	if err != nil {
		t.Fatal(err)
	}
	causes := []struct {
		name string
		err  error
	}{
		{name: "query sentinel", err: errors.Join(web.ErrInvalidQuery, io.ErrUnexpectedEOF)},
		{name: "filter sentinel", err: errors.Join(web.ErrUnsupportedFilter, io.ErrUnexpectedEOF)},
		{name: "format sentinel", err: errors.Join(web.ErrUnsupportedFormat, io.ErrUnexpectedEOF)},
		{name: "foreign outcome", err: foreign},
		{name: "joined foreign outcome", err: errors.Join(foreign, io.ErrUnexpectedEOF)},
		{name: "foreign cancellation", err: context.Canceled},
		{name: "foreign deadline", err: context.DeadlineExceeded},
	}
	for _, provider := range testProviders {
		for _, failure := range causes {
			for _, phase := range []string{"transport", "success body", "error body"} {
				t.Run(provider.name+"/"+failure.name+"/"+phase, func(t *testing.T) {
					calls := 0
					transport := providerTransport(func(request *http.Request) (*http.Response, error) {
						calls++
						if phase == "transport" {
							return nil, failure.err
						}
						status := http.StatusOK
						if phase == "error body" {
							status = http.StatusBadGateway
						}
						response := providerResponse(request, status, "")
						response.Body = &providerFailureBody{cause: failure.err}
						return response, nil
					})
					client, buildErr := provider.newClient(t.Context(), "https://provider.example", &http.Client{Transport: transport})
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					search, buildErr := web.NewSearchTool(client)
					if buildErr != nil {
						t.Fatal(buildErr)
					}
					type operation struct {
						executable tool.Tool
						arguments  string
					}
					operations := []operation{{executable: search, arguments: `{"query":"scope"}`}}
					if fetcher, ok := client.(web.Fetcher); ok {
						fetch, fetchErr := web.NewFetchTool(fetcher)
						if fetchErr != nil {
							t.Fatal(fetchErr)
						}
						operations = append(operations, operation{executable: fetch, arguments: `{"url":"https://example.com"}`})
					}
					for _, operation := range operations {
						t.Run(operation.executable.Definition().Name, func(t *testing.T) {
							calls = 0
							binding, invocation := prepareProviderTool(t, operation.executable, operation.arguments)
							_, callErr := operation.executable.Call(t.Context(), invocation)
							if _, definite := tool.ClassifyFailure(callErr); definite || callErr == nil || calls != 1 {
								t.Fatalf("external diagnostic became a tool outcome: calls=%d error=%v", calls, callErr)
							}
							if _, leaked := errors.AsType[*tool.Failure](callErr); leaked || errors.Is(callErr, failure.err) {
								t.Fatalf("external diagnostic crossed the outcome boundary: %v", callErr)
							}
							if !errors.Is(providerDiagnostic(t, callErr), failure.err) {
								t.Fatalf("external diagnostic was lost: %v", callErr)
							}
							_, callErr = binding.Call(t.Context(), invocation)
							settled, definite := tool.ClassifyFailure(callErr)
							if !definite || settled == foreign || calls != 2 {
								t.Fatalf("safe binding did not own the complete outcome: calls=%d error=%v", calls, callErr)
							}
							want := fmt.Sprintf("tool %q failed", operation.executable.Definition().Name)
							if feedback, _ := settled.Output().Text(); feedback != want || settled.Kind() != tool.FailureKindFailed {
								t.Fatalf("external diagnostic reached model feedback: %q, want %q", feedback, want)
							}
							if !errors.Is(providerDiagnostic(t, settled.Cause()), failure.err) {
								t.Fatalf("settlement lost the provider diagnostic: %v", callErr)
							}
						})
					}
				})
			}
		}
	}
}

func TestProviderAdmissionOwnsCorrectableFailuresBeforeIO(t *testing.T) {
	for _, provider := range testProviders {
		t.Run(provider.name, func(t *testing.T) {
			calls := 0
			client, err := provider.newClient(t.Context(), "https://provider.example", &http.Client{Transport: providerTransport(func(*http.Request) (*http.Response, error) {
				calls++
				return nil, errors.New("rejected input reached HTTP")
			})})
			if err != nil {
				t.Fatal(err)
			}
			search, err := web.NewSearchTool(client)
			if err != nil {
				t.Fatal(err)
			}
			type searchAdmission struct {
				request  web.SearchRequest
				want     error
				feedback string
			}
			searchCases := []searchAdmission{
				{request: web.SearchRequest{}, want: web.ErrEmptyQuery, feedback: provider.name + ": prepare search request: web: search query must not be empty"},
			}
			switch provider.name {
			case "brave":
				searchCases = append(searchCases,
					searchAdmission{web.SearchRequest{Query: "scope", Recency: web.RecencyHour}, web.ErrUnsupportedFilter, "brave: web: search filter is not supported by provider: hour recency"},
					searchAdmission{web.SearchRequest{Query: strings.Repeat("a", 601)}, web.ErrInvalidQuery, "brave: web: invalid search query: final query must contain at most 600 characters and 75 words"},
					searchAdmission{web.SearchRequest{Query: strings.Repeat("word ", 76)}, web.ErrInvalidQuery, "brave: web: invalid search query: final query must contain at most 600 characters and 75 words"},
					searchAdmission{web.SearchRequest{Query: strings.Repeat("a", 595), AllowedDomains: []string{"example.com"}}, web.ErrInvalidQuery, "brave: web: invalid search query: final query must contain at most 600 characters and 75 words"})
			case "tavily":
				searchCases = append(searchCases,
					searchAdmission{web.SearchRequest{Query: "scope", Recency: web.RecencyHour}, web.ErrUnsupportedFilter, "tavily: web: search filter is not supported by provider: hour recency"},
					searchAdmission{web.SearchRequest{Query: strings.Repeat("a", 401)}, web.ErrInvalidQuery, "tavily: web: invalid search query: query must contain at most 400 characters"})
			case "jina":
				searchCases = append(searchCases,
					searchAdmission{web.SearchRequest{Query: "scope", BlockedDomains: []string{"example.com"}}, web.ErrUnsupportedFilter, "jina: web: search filter is not supported by provider: blocked_domains"},
					searchAdmission{web.SearchRequest{Query: "scope", Recency: web.RecencyDay}, web.ErrUnsupportedFilter, "jina: web: search filter is not supported by provider: recency"})
			}
			for _, rejection := range searchCases {
				response, callErr := client.Search(t.Context(), &rejection.request)
				assertProviderAdmission(t, callErr, rejection.want, rejection.feedback)
				if response != nil || calls != 0 {
					t.Fatalf("invalid search reached provider: response=%v calls=%d", response, calls)
				}
				if rejection.request.Query == "" {
					continue
				}
				arguments, marshalErr := jsonv2.Marshal(rejection.request)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				binding, invocation := prepareProviderTool(t, search, string(arguments))
				_, callErr = binding.Call(t.Context(), invocation)
				assertProviderAdmission(t, callErr, rejection.want, rejection.feedback)
				if calls != 0 {
					t.Fatalf("local search admission performed %d HTTP calls", calls)
				}
			}
			fetcher, ok := client.(web.Fetcher)
			if !ok {
				return
			}
			fetch, err := web.NewFetchTool(fetcher)
			if err != nil {
				t.Fatal(err)
			}
			format := web.ContentFormat("invalid")
			want := web.ErrInvalidFormat
			feedback := provider.name + ": prepare fetch request: web: content format must be markdown, html, or text"
			switch provider.name {
			case "exa":
				format, want = web.FormatText, web.ErrUnsupportedFormat
				feedback = "exa: web: content format is not supported by provider: plain text is not available; use markdown or html"
			case "firecrawl":
				format, want = web.FormatText, web.ErrUnsupportedFormat
				feedback = "firecrawl: web: content format is not supported by provider: text"
			case "tavily":
				format, want = web.FormatHTML, web.ErrUnsupportedFormat
				feedback = "tavily: web: content format is not supported by provider: html"
			}
			request := &web.FetchRequest{URL: "https://example.com", Format: format}
			response, callErr := fetcher.Fetch(t.Context(), request)
			assertProviderAdmission(t, callErr, want, feedback)
			if response != nil || calls != 0 {
				t.Fatalf("invalid fetch reached provider: response=%v calls=%d", response, calls)
			}
			if errors.Is(want, web.ErrUnsupportedFormat) {
				arguments, marshalErr := jsonv2.Marshal(request)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				binding, invocation := prepareProviderTool(t, fetch, string(arguments))
				_, callErr = binding.Call(t.Context(), invocation)
				assertProviderAdmission(t, callErr, want, feedback)
			}
			if calls != 0 {
				t.Fatalf("local tool admission performed %d HTTP calls", calls)
			}
		})
	}
}

func assertProviderAdmission(t *testing.T, err error, want error, wantFeedback string) {
	t.Helper()
	failure, definite := tool.ClassifyFailure(err)
	if !definite || failure.Kind() != tool.FailureKindFailed || !errors.Is(failure.Cause(), want) || errors.Is(err, want) {
		t.Fatalf("local admission did not own its diagnostic: error=%v want=%v", err, want)
	}
	if feedback, _ := failure.Output().Text(); feedback != wantFeedback {
		t.Fatalf("local admission feedback=%q, want %q", feedback, wantFeedback)
	}
}

func TestProviderCancellationCannotBecomeModelFeedback(t *testing.T) {
	for _, phase := range []string{"transport", "body"} {
		for _, cancellation := range []string{"cancel", "deadline"} {
			t.Run(phase+"/"+cancellation, func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					ctx, cancel := context.WithTimeout(t.Context(), time.Second)
					defer cancel()
					want := context.DeadlineExceeded
					beforeError := func() { <-ctx.Done() }
					if cancellation == "cancel" {
						want = context.Canceled
						beforeError = cancel
					}
					foreign, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindRejected, Output: tool.NewTextOutput("foreign refusal")})
					if err != nil {
						t.Fatal(err)
					}
					cause := errors.Join(web.ErrInvalidQuery, foreign, io.ErrUnexpectedEOF)
					calls := 0
					transport := providerTransport(func(request *http.Request) (*http.Response, error) {
						calls++
						if phase == "transport" {
							beforeError()
							return nil, cause
						}
						response := providerResponse(request, http.StatusOK, "")
						response.Body = &providerFailureBody{cause: cause, beforeRead: beforeError}
						return response, nil
					})
					client, err := brave.NewClient(t.Context(), brave.Config{APIKey: "test", BaseURL: "https://provider.example", HTTPClient: &http.Client{Transport: transport}})
					if err != nil {
						t.Fatal(err)
					}
					search, err := web.NewSearchTool(client)
					if err != nil {
						t.Fatal(err)
					}
					binding, invocation := prepareProviderTool(t, search, `{"query":"scope"}`)
					_, callErr := binding.Call(ctx, invocation)
					if _, definite := tool.ClassifyFailure(callErr); definite || !errors.Is(callErr, want) || calls != 1 {
						t.Fatalf("cancellation became tool feedback: calls=%d error=%v want=%v", calls, callErr, want)
					}
					if _, leaked := errors.AsType[*tool.Failure](callErr); leaked || !errors.Is(providerDiagnostic(t, callErr), cause) {
						t.Fatalf("cancellation lost the diagnostic boundary: %v", callErr)
					}
				})
			})
		}
	}
}
