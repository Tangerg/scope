package web_test

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/tools/web"
	"github.com/Tangerg/scope/tools/web/brave"
)

type providerTransport func(*http.Request) (*http.Response, error)

func (p providerTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return p(request)
}

func providerResponse(request *http.Request, status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}
}

func TestSearchProvidersAdmitResultsAgainstTheRequest(t *testing.T) {
	for _, provider := range testProviders {
		t.Run(provider.name, func(t *testing.T) {
			transport := providerTransport(func(request *http.Request) (*http.Response, error) {
				return providerResponse(request, http.StatusOK, provider.searchPayload(2)), nil
			})
			client, err := provider.newClient(t.Context(), "https://provider.example", &http.Client{Transport: transport})
			if err != nil {
				t.Fatal(err)
			}
			response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope", MaxResults: 1})
			if response != nil || !errors.Is(err, web.ErrInvalidSearchResponse) {
				t.Fatalf("explicit maximum: response=%v error=%v", response, err)
			}
			response, err = client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
			if err != nil || response == nil || len(response.Results) != 2 {
				t.Fatalf("provider default: response=%v error=%v", response, err)
			}
		})
	}
}

func TestProvidersKeepBorrowedHTTPClientsUnchanged(t *testing.T) {
	for _, provider := range testProviders {
		t.Run(provider.name, func(t *testing.T) {
			borrowed := &http.Client{Timeout: time.Second}
			if _, err := provider.newClient(t.Context(), "https://provider.example", borrowed); err != nil {
				t.Fatal(err)
			}
			if borrowed.Transport != nil || borrowed.CheckRedirect != nil || borrowed.Timeout != time.Second {
				t.Fatalf("caller-owned client changed: %+v", borrowed)
			}
		})
	}
}

func TestProviderCredentialsStayOnTheirAPIOrigin(t *testing.T) {
	for _, provider := range testProviders {
		for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
			for _, target := range []string{"https://other.example/page", "http://provider.example/page", "https://provider.example:444/page", "https://provider.example/page"} {
				t.Run(fmt.Sprintf("%s/%d/%s", provider.name, status, target), func(t *testing.T) {
					calls := 0
					transport := providerTransport(func(request *http.Request) (*http.Response, error) {
						calls++
						if calls == 1 {
							response := providerResponse(request, status, "")
							response.Header.Set("Location", target)
							return response, nil
						}
						if request.Header.Get("Authorization") == "" && request.Header.Get("X-API-Key") == "" && request.Header.Get("X-Subscription-Token") == "" {
							t.Error("same-origin redirect lost its credential")
						}
						return providerResponse(request, http.StatusOK, provider.searchPayload(0)), nil
					})
					client, err := provider.newClient(t.Context(), "https://provider.example", &http.Client{Transport: transport})
					if err != nil {
						t.Fatal(err)
					}
					response, err := client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
					if target == "https://provider.example/page" {
						if err != nil || response == nil || calls != 2 {
							t.Fatalf("same origin: response=%v error=%v calls=%d", response, err, calls)
						}
					} else if err == nil || response != nil || calls != 1 {
						t.Fatalf("untrusted origin reached transport: response=%v error=%v calls=%d", response, err, calls)
					}
				})
			}
		}
	}
}

func TestProviderOriginGuardComposesWithHostRedirectPolicy(t *testing.T) {
	denied := errors.New("host denied redirect")
	for _, rewrite := range []bool{false, true} {
		t.Run(fmt.Sprint(rewrite), func(t *testing.T) {
			calls, policyCalls := 0, 0
			borrowed := &http.Client{
				Transport: providerTransport(func(request *http.Request) (*http.Response, error) {
					calls++
					response := providerResponse(request, http.StatusTemporaryRedirect, "")
					response.Header.Set("Location", "https://provider.example/redirect")
					return response, nil
				}),
				CheckRedirect: func(request *http.Request, _ []*http.Request) error {
					policyCalls++
					if !rewrite {
						return denied
					}
					request.URL, _ = url.Parse("https://other.example/changed-by-host")
					return nil
				},
			}
			client, err := brave.NewClient(t.Context(), brave.Config{APIKey: "test-key", BaseURL: "https://provider.example", HTTPClient: borrowed})
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.Search(t.Context(), &web.SearchRequest{Query: "scope"})
			if err == nil || calls != 1 || policyCalls != 1 || !rewrite && !errors.Is(err, denied) {
				t.Fatalf("host policy: error=%v transport=%d policy=%d", err, calls, policyCalls)
			}
		})
	}
}

func TestFetchProvidersRespectRequestedURLOutcomes(t *testing.T) {
	for _, provider := range testProviders {
		if provider.name != "tavily" && provider.name != "exa" {
			continue
		}
		for _, failed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/failure=%t", provider.name, failed), func(t *testing.T) {
				body := `{"results":[{"url":"https://canonical.example/page","text":"page"}],"statuses":[{"id":"https://example.com/requested","status":"success"}]}`
				if provider.name == "tavily" {
					body = `{"results":[{"url":"https://canonical.example/page","raw_content":"page"}],"failed_results":[]}`
				}
				if failed {
					body = `{"results":[{"url":"https://unrelated.example/page","text":"wrong page"}],"statuses":[{"id":"https://example.com/requested","status":"error","error":{"tag":"CRAWL_NOT_FOUND","httpStatusCode":404}}]}`
					if provider.name == "tavily" {
						body = `{"results":[{"url":"https://unrelated.example/page","raw_content":"wrong page"}],"failed_results":[{"url":"https://example.com/requested","error":"not found"}]}`
					}
				}
				transport := providerTransport(func(request *http.Request) (*http.Response, error) {
					return providerResponse(request, http.StatusOK, body), nil
				})
				client, err := provider.newClient(t.Context(), "https://provider.example", &http.Client{Transport: transport})
				if err != nil {
					t.Fatal(err)
				}
				response, err := client.(web.Fetcher).Fetch(t.Context(), &web.FetchRequest{URL: "https://example.com/requested"})
				if failed {
					if response != nil || err == nil {
						t.Fatalf("requested failure became success: response=%v error=%v", response, err)
					}
				} else if err != nil || response == nil || response.Content != "page" {
					t.Fatalf("canonical destination: response=%v error=%v", response, err)
				}
			})
		}
	}
}
