package luma_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/luma"
)

func TestImageModel_Call_Mock(t *testing.T) {
	var polls modeltest.PollCounter

	var server *httptest.Server
	server = modeltest.MuxServer(
		modeltest.Route{Method: "POST", Contains: "/generations", Handle: func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "Bearer test-key" {
				t.Errorf("Authorization = %q", r.Header.Get("Authorization"))
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			w.Write([]byte(`{"id":"gen-1","created_at":"2026-07-31T08:00:00Z","model":"uni-1","state":"queued","type":"image","output":[]}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/generations/", Handle: func(w http.ResponseWriter, r *http.Request) {
			n := polls.Inc()
			state := "processing"
			output := "[]"
			if n >= 2 {
				state = "completed"
				output = fmt.Sprintf(`[{"type":"image","url":%q}]`, server.URL+"/output.png")
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"id":"gen-1","created_at":"2026-07-31T08:00:00Z","model":"uni-1","state":"` + state + `","type":"image","output":` + output + `}`))
		}},
		modeltest.Route{Method: "GET", Contains: "/output.png", Handle: func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write([]byte("PNG"))
		}},
	)
	t.Cleanup(server.Close)

	opts := image.Options{Model: luma.ModelUni1}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        server.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a serene mountain lake")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil || string(out.First().Media.Source.Bytes) != "PNG" {
		t.Fatalf("result = %#v", out.First())
	}

	limited, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        server.URL,
		PollInterval:   10 * time.Millisecond,
		PollTimeout:    5 * time.Second,
		MaxOutputBytes: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = limited.Call(t.Context(), req); err == nil || !strings.Contains(err.Error(), "2-byte limit") {
		t.Fatalf("limited Call error = %v", err)
	}
}

func TestImageModelConfigRejectsNegativeOutputLimit(t *testing.T) {
	opts := image.Options{Model: luma.ModelUni1}
	_, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{
		APIKey: "test-key", DefaultOptions: opts, MaxOutputBytes: -1,
	})
	if err == nil {
		t.Fatal("NewImageModel accepted a negative output limit")
	}
}

func TestImageRejectsNullGenerationAtSubmissionAndPoll(t *testing.T) {
	for _, poll := range []bool{false, true} {
		t.Run(fmt.Sprint(poll), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if poll && r.Method == http.MethodPost {
					_, _ = w.Write([]byte(`{"id":"generation","state":"queued","type":"image","model":"uni-1","output":[]}`))
				} else {
					_, _ = w.Write([]byte(`null`))
				}
			}))
			defer server.Close()
			model, err := luma.NewImageModel(t.Context(), luma.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: luma.ModelUni1}, PollInterval: time.Millisecond})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := image.NewRequest("image")
			if response, err := model.Call(t.Context(), request); response != nil || err == nil {
				t.Fatalf("null generation=%v, %v", response, err)
			}
		})
	}
}
