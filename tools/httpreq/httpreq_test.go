package httpreq

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestToolUsesStrictTypedContract(t *testing.T) {
	client, err := NewClient(ClientConfig{AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewTool(client)
	if err != nil {
		t.Fatal(err)
	}
	definition := tool.Definition()
	if definition.Name != "http_request" {
		t.Fatalf("name = %q, want http_request", definition.Name)
	}
	var schema struct {
		AdditionalProperties bool `json:"additionalProperties"`
	}
	if decodeErr := json.Unmarshal(definition.InputSchema, &schema); decodeErr != nil {
		t.Fatalf("decode schema: %v", decodeErr)
	}
	if schema.AdditionalProperties {
		t.Fatalf("schema permits unknown fields: %s", definition.InputSchema)
	}
	binding, err := toolcontract.Bind(tool)
	if err != nil {
		t.Fatal(err)
	}
	for _, arguments := range []string{
		`{"url":"https://example.com","timeout":5000}`,
		`{"url":"https://example.com","method":"get"}`,
		`{"url":"https://example.com","timeout_ms":120001}`,
		`{"url":"https://example.com","timeout_ms":1.0}`,
	} {
		if _, err := binding.Contract().Prepare(chat.ToolCall{ID: "request", Name: definition.Name, Arguments: arguments}); !errors.Is(err, toolcontract.ErrInvalidInvocation) {
			t.Errorf("Prepare(%s) = %v, want invalid invocation", arguments, err)
		}
	}
}

func TestNewClient_RequiresAllowlist(t *testing.T) {
	if _, err := NewClient(ClientConfig{}); !errors.Is(err, ErrMissingAllowedHosts) {
		t.Fatalf("want ErrMissingAllowedHosts, got %v", err)
	}
}

func TestClientConfigValidate(t *testing.T) {
	tests := []struct {
		name   string
		config ClientConfig
		want   error
	}{
		{name: "valid", config: ClientConfig{AllowedHosts: []string{"example.com"}}},
		{name: "missing hosts", config: ClientConfig{}, want: ErrMissingAllowedHosts},
		{name: "invalid host", config: ClientConfig{AllowedHosts: []string{"bad*host"}}, want: ErrInvalidClientConfig},
		{name: "host URL", config: ClientConfig{AllowedHosts: []string{"https://example.com"}}, want: ErrInvalidHostPattern},
		{name: "host with port", config: ClientConfig{AllowedHosts: []string{"example.com:443"}}, want: ErrInvalidHostPattern},
		{name: "wildcard IP", config: ClientConfig{AllowedHosts: []string{"*.127.0.0.1"}}, want: ErrInvalidHostPattern},
		{name: "blank method", config: ClientConfig{AllowedHosts: []string{"example.com"}, AllowedMethods: []Method{""}}, want: ErrInvalidClientConfig},
		{name: "unsupported method", config: ClientConfig{AllowedHosts: []string{"example.com"}, AllowedMethods: []Method{"CONNECT"}}, want: ErrInvalidMethod},
		{name: "negative timeout", config: ClientConfig{AllowedHosts: []string{"example.com"}, DefaultTimeout: -time.Second}, want: ErrInvalidClientConfig},
		{name: "excessive timeout", config: ClientConfig{AllowedHosts: []string{"example.com"}, DefaultTimeout: MaxRequestTimeout + time.Nanosecond}, want: ErrInvalidClientConfig},
		{name: "negative response limit", config: ClientConfig{AllowedHosts: []string{"example.com"}, MaxResponseBytes: -1}, want: ErrInvalidClientConfig},
		{name: "response limit overflow", config: ClientConfig{AllowedHosts: []string{"example.com"}, MaxResponseBytes: maxSupportedResponseBytes + 1}, want: ErrInvalidClientConfig},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.config.Validate()
			if test.want == nil && err != nil {
				t.Fatalf("Validate() error = %v", err)
			}
			if test.want != nil && !errors.Is(err, test.want) {
				t.Fatalf("Validate() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestRedirectPolicyRejectsNonHTTPURL(t *testing.T) {
	policy, err := (ClientConfig{AllowedHosts: []string{"example.com"}}).compilePolicy()
	if err != nil {
		t.Fatal(err)
	}
	request := &http.Request{Method: http.MethodGet, URL: &url.URL{Scheme: "ftp", Host: "example.com"}}
	if err := policy.checkRedirect(request, nil); !errors.Is(err, ErrInvalidURL) {
		t.Fatalf("checkRedirect error = %v, want ErrInvalidURL", err)
	}
}

func TestMethod(t *testing.T) {
	if got := Method("").Normalize(); got != MethodGET {
		t.Fatalf("empty Method.Normalize() = %q, want %q", got, MethodGET)
	}
	if got := Method(" post ").Normalize(); got != MethodPOST {
		t.Fatalf("Method(post).Normalize() = %q, want %q", got, MethodPOST)
	}
	if err := Method("CONNECT").Validate(); !errors.Is(err, ErrInvalidMethod) {
		t.Fatalf("Method(CONNECT).Validate() error = %v, want ErrInvalidMethod", err)
	}
}

func TestAllowlistNormalizesDNSAndIPHosts(t *testing.T) {
	allowlist, err := NewAllowlist([]string{"BÜCHER.example.", "[2001:db8::1]"})
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"bücher.example", "xn--bcher-kva.example.", "2001:db8::1"} {
		if !allowlist.Allows(host) {
			t.Errorf("normalized host %q should be allowed", host)
		}
	}
}

func TestRequestPrepareReturnsOwnedNormalizedCopy(t *testing.T) {
	original := &Request{
		URL:     "  https://example.com/path  ",
		Method:  " post ",
		Headers: map[string]string{"X-Test": "original"},
		Query:   map[string]string{"q": "scope"},
	}
	if err := original.Validate(); err != nil {
		t.Fatal(err)
	}
	prepared, err := original.prepare()
	if err != nil {
		t.Fatal(err)
	}
	prepared.Headers["X-Test"] = "changed"
	prepared.Query["q"] = "changed"

	if prepared.URL != "https://example.com/path" || prepared.Method != MethodPOST {
		t.Fatalf("prepared request = %#v", prepared)
	}
	if original.URL != "  https://example.com/path  " || original.Method != " post " ||
		original.Headers["X-Test"] != "original" || original.Query["q"] != "scope" {
		t.Fatalf("Prepare mutated its input: %#v", original)
	}
}

func TestDo_HostAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("X-Multi", "first")
		w.Header().Add("X-Multi", "second")
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	srvURL := srv.URL
	hostOnly, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(ClientConfig{AllowedHosts: []string{hostOnly}})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(t.Context(), &Request{URL: srvURL + "/x"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != 200 || resp.Body != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if values := resp.Headers["X-Multi"]; len(values) != 2 || values[0] != "first" || values[1] != "second" {
		t.Fatalf("multi-value response header = %v", values)
	}

	if _, err := client.Do(t.Context(), &Request{URL: "https://blocked.example.com/x"}); !errors.Is(err, ErrHostNotAllowed) {
		t.Fatalf("want ErrHostNotAllowed, got %v", err)
	}
}

func TestNilClientRejectsRequest(t *testing.T) {
	var client *Client
	if _, err := client.Do(t.Context(), &Request{URL: "https://example.com"}); !errors.Is(err, ErrNilClient) {
		t.Fatalf("nil Client.Do error = %v, want ErrNilClient", err)
	}
}

func TestDo_RedirectHostAllowlist(t *testing.T) {
	t.Run("allows redirect to permitted host", func(t *testing.T) {
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "redirected")
		}))
		t.Cleanup(target.Close)
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL, http.StatusFound)
		}))
		t.Cleanup(source.Close)

		client, err := NewClient(ClientConfig{AllowedHosts: []string{testURLHostname(t, source.URL)}})
		if err != nil {
			t.Fatal(err)
		}
		resp, err := client.Do(t.Context(), &Request{URL: source.URL})
		if err != nil {
			t.Fatalf("follow permitted redirect: %v", err)
		}
		if resp.Body != "redirected" {
			t.Fatalf("body = %q, want redirected", resp.Body)
		}
	})

	t.Run("rejects redirect to blocked host", func(t *testing.T) {
		var targetHit atomic.Bool
		target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			targetHit.Store(true)
			_, _ = io.WriteString(w, "secret")
		}))
		t.Cleanup(target.Close)
		blockedTarget := testURLWithHostname(t, target.URL, "localhost")
		source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, blockedTarget, http.StatusFound)
		}))
		t.Cleanup(source.Close)

		callerPolicyErr := errors.New("caller redirect policy")
		httpClient := &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return callerPolicyErr
			},
		}
		client, err := NewClient(ClientConfig{
			AllowedHosts: []string{testURLHostname(t, source.URL)},
			HTTPClient:   httpClient,
		})
		if err != nil {
			t.Fatal(err)
		}
		if checkRedirectErr := httpClient.CheckRedirect(nil, nil); !errors.Is(checkRedirectErr, callerPolicyErr) {
			t.Fatalf("NewClient mutated caller-owned redirect policy: %v", checkRedirectErr)
		}
		_, err = client.Do(t.Context(), &Request{URL: source.URL})
		if !errors.Is(err, ErrHostNotAllowed) {
			t.Fatalf("redirect error = %v, want ErrHostNotAllowed", err)
		}
		if targetHit.Load() {
			t.Fatal("blocked redirect reached its target")
		}
	})
}

func TestDo_RedirectRevalidatesMethod(t *testing.T) {
	var targetHits atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/found":
			http.Redirect(w, r, "/target", http.StatusFound)
		case "/temporary":
			http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
		case "/target":
			targetHits.Add(1)
			_, _ = io.WriteString(w, r.Method)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	client, err := NewClient(ClientConfig{
		AllowedHosts:   []string{testURLHostname(t, srv.URL)},
		AllowedMethods: []Method{MethodPOST},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, redirectErr := client.Do(t.Context(), &Request{URL: srv.URL + "/found", Method: MethodPOST}); !errors.Is(redirectErr, ErrMethodNotAllowed) {
		t.Fatalf("302 redirect error = %v, want ErrMethodNotAllowed", redirectErr)
	}
	if targetHits.Load() != 0 {
		t.Fatal("redirect with rewritten, disallowed method reached its target")
	}

	response, err := client.Do(t.Context(), &Request{URL: srv.URL + "/temporary", Method: MethodPOST})
	if err != nil {
		t.Fatalf("307 redirect: %v", err)
	}
	if response.Body != string(MethodPOST) || targetHits.Load() != 1 {
		t.Fatalf("307 response = %#v, target hits = %d", response, targetHits.Load())
	}
}

func TestDo_WildcardHost(t *testing.T) {
	client, err := NewClient(ClientConfig{AllowedHosts: []string{"*.example.com"}})
	if err != nil {
		t.Fatal(err)
	}

	if !client.allowedHosts.Allows("api.example.com") {
		t.Error("api.example.com should match *.example.com")
	}
	if !client.allowedHosts.Allows("a.b.example.com") {
		t.Error("a.b.example.com should match *.example.com")
	}
	if client.allowedHosts.Allows("example.com") {
		t.Error("example.com should NOT match *.example.com (suffix-only)")
	}
	if client.allowedHosts.Allows("evilexample.com") {
		t.Error("evilexample.com should NOT match *.example.com")
	}
}

func TestDo_MethodAllowlist(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(r.Method))
	}))
	t.Cleanup(srv.Close)

	hostOnly, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(ClientConfig{AllowedHosts: []string{hostOnly}})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Do(t.Context(), &Request{URL: srv.URL, Method: "POST"})
	if !errors.Is(err, ErrMethodNotAllowed) {
		t.Fatalf("default methods should block POST, got %v", err)
	}

	writeClient, err := NewClient(ClientConfig{
		AllowedHosts:   []string{hostOnly},
		AllowedMethods: []Method{MethodGET, MethodPOST},
	})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := writeClient.Do(t.Context(), &Request{URL: srv.URL, Method: "post"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Body != "POST" {
		t.Fatalf("server saw method %q, want POST", resp.Body)
	}
}

func TestDo_ValidatesMethodAndTimeout(t *testing.T) {
	client, err := NewClient(ClientConfig{AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		request Request
		want    error
	}{
		{name: "method", request: Request{URL: "https://example.com", Method: "CONNECT"}, want: ErrInvalidMethod},
		{name: "negative timeout", request: Request{URL: "https://example.com", TimeoutMS: -1}, want: ErrInvalidRequestTimeout},
		{name: "large timeout", request: Request{URL: "https://example.com", TimeoutMS: int(MaxRequestTimeout/time.Millisecond) + 1}, want: ErrInvalidRequestTimeout},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := client.Do(t.Context(), &test.request); !errors.Is(err, test.want) {
				t.Fatalf("Do() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestDo_ResponseTruncation(t *testing.T) {
	payload := strings.Repeat("x", 1000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, payload)
	}))
	t.Cleanup(srv.Close)

	hostOnly, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}

	client, err := NewClient(ClientConfig{
		AllowedHosts:     []string{hostOnly},
		MaxResponseBytes: 100,
	})
	if err != nil {
		t.Fatal(err)
	}

	resp, err := client.Do(t.Context(), &Request{URL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	if !resp.Truncated {
		t.Error("expected Truncated=true")
	}
	if len(resp.Body) != 100 {
		t.Errorf("body length = %d, want 100", len(resp.Body))
	}
}

func TestDo_InvalidURL(t *testing.T) {
	client, err := NewClient(ClientConfig{AllowedHosts: []string{"example.com"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{"", "ftp://example.com", "not-a-url", "/relative"} {
		if _, err := client.Do(t.Context(), &Request{URL: bad}); err == nil {
			t.Errorf("URL %q should be rejected", bad)
		}
	}
}

func TestDo_DefaultHeadersAndQuery(t *testing.T) {
	var sawAuth, sawQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization")
		sawQuery = r.URL.Query().Get("q")
		w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)

	hostOnly, _, err := net.SplitHostPort(srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(ClientConfig{
		AllowedHosts:   []string{hostOnly},
		DefaultHeaders: map[string]string{"Authorization": "Bearer secret"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = client.Do(t.Context(), &Request{
		URL:   srv.URL,
		Query: map[string]string{"q": "hello"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if sawAuth != "Bearer secret" {
		t.Errorf("DefaultHeaders not applied; got %q", sawAuth)
	}
	if sawQuery != "hello" {
		t.Errorf("Query not applied; got %q", sawQuery)
	}
}

func testURLHostname(t *testing.T, rawURL string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	return parsed.Hostname()
}

func testURLWithHostname(t *testing.T, rawURL, hostname string) string {
	t.Helper()
	parsed, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse test URL: %v", err)
	}
	parsed.Host = net.JoinHostPort(hostname, parsed.Port())
	return parsed.String()
}
