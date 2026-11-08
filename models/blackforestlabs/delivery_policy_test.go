package blackforestlabs_test

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
	"github.com/Tangerg/scope/models/blackforestlabs"
)

type deliveryPolicyTransport func(*http.Request) (*http.Response, error)

func (d deliveryPolicyTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	return d(request)
}

func TestOutputAuthorityIsIndependentFromAPIOrigin(t *testing.T) {
	for _, test := range []struct {
		name, output string
		origins      []string
		allowed      bool
	}{
		{"official Azure delivery", "https://bfldeliveryprod.blob.core.windows.net/output.png?sig=signature", nil, true},
		{"official regional delivery", "https://delivery.us1.bfl.ai/output.png", nil, true},
		{"unrelated Azure tenant", "https://unrelated.blob.core.windows.net/output.png", nil, false},
		{"ungranted API origin", "https://api.test/output.png", nil, false},
		{"explicit delivery origin", "https://delivery.test/output.png", []string{"https://delivery.test"}, true},
		{"ungranted custom origin", "https://delivery.test/output.png", nil, false},
		{"official hostname foreign port", "https://delivery.us1.bfl.ai:8443/output.png", nil, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			downloads := 0
			client := &http.Client{Transport: deliveryPolicyTransport(func(request *http.Request) (*http.Response, error) {
				contentType, body := "application/json", ""
				switch {
				case request.Method == http.MethodPost:
					body = `{"id":"task","polling_url":"https://api.test/poll"}`
				case request.URL.Path == "/poll":
					body = fmt.Sprintf(`{"id":"task","status":"Ready","result":{"sample":%q}}`, test.output)
				default:
					downloads++
					if request.URL.String() != test.output || request.Header.Get("x-key") != "" {
						t.Errorf("download=%s headers=%v", request.URL, request.Header)
					}
					contentType, body = "image/png", "PNG"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "secret", BaseURL: "https://api.test", HTTPClient: client, OutputOrigins: test.origins, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"})
			if test.allowed {
				if err != nil || response == nil || downloads != 1 {
					t.Fatalf("response=%v error=%v downloads=%d", response, err, downloads)
				}
			} else if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || downloads != 0 {
				t.Fatalf("response=%v error=%v downloads=%d", response, err, downloads)
			}
		})
	}
}

func TestOutputRedirectCannotReintroduceCredentials(t *testing.T) {
	origin, err := url.Parse("https://api.test")
	if err != nil {
		t.Fatal(err)
	}
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	jar.SetCookies(origin, []*http.Cookie{{Name: "session", Value: "secret"}})
	downloads := 0
	client := &http.Client{Jar: jar, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
		for _, header := range []string{"x-key", "Authorization", "Proxy-Authorization", "Cookie"} {
			request.Header.Set(header, "secret")
		}
		return nil
	}, Transport: deliveryPolicyTransport(func(request *http.Request) (*http.Response, error) {
		status, header, body := http.StatusOK, http.Header{"Content-Type": {"application/json"}}, ""
		switch {
		case request.Method == http.MethodPost:
			body = `{"id":"task","polling_url":"https://api.test/poll"}`
		case request.URL.Path == "/poll":
			body = `{"id":"task","status":"Ready","result":{"sample":"https://api.test/output.png"}}`
		default:
			downloads++
			for _, credential := range []string{"x-key", "Authorization", "Proxy-Authorization", "Cookie"} {
				if request.Header.Get(credential) != "" {
					t.Errorf("output leaked %s", credential)
				}
			}
			if request.URL.Path == "/output.png" {
				status = http.StatusTemporaryRedirect
				header.Set("Location", "https://api.test/final.png")
			} else {
				header.Set("Content-Type", "image/png")
				body = "PNG"
			}
		}
		return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
	})}
	model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "secret", BaseURL: origin.String(), HTTPClient: client, OutputOrigins: []string{origin.String()}, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"}); err != nil {
		t.Fatal(err)
	}
	if downloads != 2 {
		t.Fatalf("downloads=%d", downloads)
	}
	if client.Jar != jar || len(jar.Cookies(origin)) != 1 {
		t.Fatal("borrowed cookie jar changed")
	}
}

func TestDeliveryPermissionCannotGrantPollingCredentials(t *testing.T) {
	for _, origin := range []string{"https://bfldeliveryprod.blob.core.windows.net", "https://delivery.test"} {
		t.Run(origin, func(t *testing.T) {
			attempts := 0
			client := &http.Client{Transport: deliveryPolicyTransport(func(request *http.Request) (*http.Response, error) {
				attempts++
				body := fmt.Sprintf(`{"id":"task","polling_url":%q}`, origin+"/poll")
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "secret", BaseURL: "https://api.test", HTTPClient: client, OutputOrigins: []string{origin}, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				t.Fatal(err)
			}
			if response, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"}); response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || attempts != 1 {
				t.Fatalf("response=%v error=%v attempts=%d", response, err, attempts)
			}
		})
	}
}

func TestOutputRedirectPolicyRemainsBoundedAfterHostCallback(t *testing.T) {
	for _, downgrade := range []bool{false, true} {
		name := "redirect loop"
		if downgrade {
			name = "HTTPS downgrade"
		}
		t.Run(name, func(t *testing.T) {
			downloads := 0
			client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return nil }, Transport: deliveryPolicyTransport(func(request *http.Request) (*http.Response, error) {
				header, body, status := http.Header{"Content-Type": {"application/json"}}, "", http.StatusOK
				switch {
				case request.Method == http.MethodPost:
					body = `{"id":"task","polling_url":"https://api.bfl.ai/poll"}`
				case request.URL.Path == "/poll":
					body = `{"id":"task","status":"Ready","result":{"sample":"https://bfldeliveryprod.blob.core.windows.net/image.png"}}`
				default:
					downloads++
					status = http.StatusTemporaryRedirect
					target := "https://bfldeliveryprod.blob.core.windows.net/image.png"
					if downgrade {
						target = "http://bfldeliveryprod.blob.core.windows.net/image.png"
					}
					header.Set("Location", target)
				}
				return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "secret", HTTPClient: client, OutputOrigins: []string{"http://bfldeliveryprod.blob.core.windows.net"}, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"})
			if response != nil || err == nil {
				t.Fatalf("response=%v error=%v", response, err)
			}
			if downgrade {
				if !errors.Is(err, httpclient.ErrOriginNotAllowed) || downloads != 1 {
					t.Fatalf("error=%v downloads=%d", err, downloads)
				}
			} else if downloads != 10 {
				t.Fatalf("downloads=%d; redirect callback bypassed limit", downloads)
			}
		})
	}
}

func TestNativeDurationPreservesFractionalValueAndPresence(t *testing.T) {
	for _, test := range []struct {
		name, duration, expected string
		invalid                  bool
	}{
		{"missing", "", "", false},
		{"zero", `,"duration":0`, "0", false},
		{"fractional", `,"duration":1.596290111541748`, "1.596290111541748", false},
		{"large integer", `,"duration":9007199254740993`, "9007199254740993", false},
		{"negative", `,"duration":-1`, "", true},
		{"overflow", `,"duration":1e999`, "", true},
		{"quoted number", `,"duration":"1.5"`, "", true},
		{"not a number", `,"duration":"NaN"`, "", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			downloads := 0
			client := &http.Client{Transport: deliveryPolicyTransport(func(request *http.Request) (*http.Response, error) {
				contentType, body := "application/json", ""
				switch {
				case request.Method == http.MethodPost:
					body = `{"id":"task","polling_url":"https://api.bfl.ai/poll"}`
				case request.URL.Path == "/poll":
					body = `{"id":"task","status":"Ready","result":{"sample":"https://bfldeliveryprod.blob.core.windows.net/image.png"` + test.duration + `}}`
				default:
					downloads++
					contentType, body = "image/png", "PNG"
				}
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
			})}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "secret", HTTPClient: client, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "landscape"})
			if test.invalid {
				if err == nil || downloads != 0 {
					t.Fatalf("error=%v downloads=%d", err, downloads)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			raw, present := response.First().Metadata["blackforestlabs/duration"]
			if present != (test.expected != "") || string(raw) != test.expected {
				t.Fatalf("duration=%s present=%t", raw, present)
			}
			if _, present := response.First().Metadata["blackforestlabs/duration_ms"]; present {
				t.Fatal("native duration assigned an undocumented unit")
			}
		})
	}
}
