package blackforestlabs_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

type regionalTransport struct {
	t              *testing.T
	redirectPoll   bool
	redirectMedia  bool
	polls          int
	downloads      int
	finalDownloads int
	foreign        int
}

func (r *regionalTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	r.t.Helper()
	status := http.StatusOK
	header := http.Header{"Content-Type": {"application/json"}}
	var body string
	switch request.URL.Host {
	case "api.bfl.ai":
		if request.Header.Get("x-key") != "sentinel" {
			r.t.Error("submission lost credential")
		}
		body = `{"id":"task","polling_url":"https://api.us1.bfl.ai/v1/get_result?id=task"}`
	case "api.us1.bfl.ai":
		r.polls++
		if request.Header.Get("x-key") != "sentinel" {
			r.t.Error("regional polling lost credential")
		}
		if r.redirectPoll {
			status = http.StatusTemporaryRedirect
			header.Set("Location", "https://api.eu1.bfl.ai/redirected")
		} else {
			body = `{"id":"task","status":"Ready","result":{"sample":"https://delivery.us1.bfl.ai/image.png"}}`
		}
	case "delivery.us1.bfl.ai", "delivery.eu1.bfl.ai":
		r.downloads++
		if request.Header.Get("x-key") != "" {
			r.t.Error("media download received API credential")
		}
		if r.redirectMedia && request.URL.Path != "/final.png" {
			status = http.StatusTemporaryRedirect
			header.Set("Location", "https://delivery.eu1.bfl.ai/final.png")
		} else {
			r.finalDownloads++
			header.Set("Content-Type", "image/png")
			body = "PNG"
		}
	default:
		r.foreign++
		status = http.StatusBadRequest
	}
	return &http.Response{StatusCode: status, Header: header, Body: io.NopCloser(strings.NewReader(body)), Request: request}, nil
}

func TestMediaDownloadsValidateFinalRequestAuthority(t *testing.T) {
	for _, forgedHost := range []bool{false, true} {
		name := "official regional redirect"
		if forgedHost {
			name = "host callback changes authority"
		}
		t.Run(name, func(t *testing.T) {
			transport := &regionalTransport{t: t, redirectMedia: true}
			client := &http.Client{Transport: transport, CheckRedirect: func(request *http.Request, _ []*http.Request) error {
				if forgedHost {
					request.Host = "outside.test"
				}
				return nil
			}}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "sentinel", HTTPClient: client, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
			if forgedHost {
				if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || transport.finalDownloads != 0 {
					t.Fatalf("forged authority: response=%v error=%v final requests=%d", response, err, transport.finalDownloads)
				}
			} else if err != nil || response.First() == nil || transport.finalDownloads != 1 {
				t.Fatalf("official delivery: response=%v error=%v final requests=%d", response, err, transport.finalDownloads)
			}
		})
	}
}

func TestMediaDownloadUsesDefaultForTypedNilTransport(t *testing.T) {
	transport := &regionalTransport{t: t}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = original }()
	var absent *http.Transport
	client := &http.Client{Transport: absent}
	model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "sentinel", HTTPClient: client, DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
	if err != nil || response.First() == nil || transport.finalDownloads != 1 {
		t.Fatalf("Call = %v, %v; downloads = %d", response, err, transport.finalDownloads)
	}
	if client.Transport != absent {
		t.Fatal("caller-owned typed-nil transport was replaced")
	}
}

func TestRegionalPollingAuthorizesOnlyItsIssuedOrigin(t *testing.T) {
	for _, test := range []struct {
		name     string
		redirect bool
	}{{name: "regional polling and media"}, {name: "poll redirect cannot expand origin", redirect: true}} {
		t.Run(test.name, func(t *testing.T) {
			transport := &regionalTransport{t: t, redirectPoll: test.redirect}
			model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{
				APIKey: "sentinel", HTTPClient: &http.Client{Transport: transport},
				DefaultOptions: image.Options{Model: blackforestlabs.ModelFlux11Pro},
				PollInterval:   time.Nanosecond, PollTimeout: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &image.Request{Prompt: "image"})
			if test.redirect {
				if response != nil || !errors.Is(err, httpclient.ErrOriginNotAllowed) || transport.downloads != 0 {
					t.Fatalf("redirect response=%v error=%v downloads=%d", response, err, transport.downloads)
				}
			} else if err != nil || response.First() == nil || transport.downloads != 1 {
				t.Fatalf("regional generation response=%v error=%v downloads=%d", response, err, transport.downloads)
			}
			if transport.polls != 1 || transport.foreign != 0 {
				t.Fatalf("polls=%d foreign requests=%d", transport.polls, transport.foreign)
			}
		})
	}
}
