package stability_test

import (
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/modeltest"
	"github.com/Tangerg/scope/models/stability"
)

func TestImageModel_Call_Mock(t *testing.T) {
	imgB64 := base64.StdEncoding.EncodeToString([]byte("FAKE-PNG-BYTES"))
	body := `{"image":"` + imgB64 + `","finish_reason":"SUCCESS","seed":42}`
	srv := modeltest.JSONServer(http.StatusOK, body)
	t.Cleanup(srv.Close)

	opts := image.Options{Model: stability.ModelCore}
	err := opts.Validate()
	if err != nil {
		t.Fatal(err)
	}
	m, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{
		APIKey:         "test-key",
		DefaultOptions: opts,
		BaseURL:        srv.URL,
	})
	if err != nil {
		t.Fatal(err)
	}

	req, _ := image.NewRequest("a small red square")
	out, err := m.Call(t.Context(), req)
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if out.First() == nil {
		t.Fatal("nil result")
	}
}

func TestImageSeedPresenceAndRange(t *testing.T) {
	for _, seed := range []string{"", `,"seed":null`, `,"seed":0`, `,"seed":42`, `,"seed":-1`, `,"seed":4294967295`} {
		t.Run(seed, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"image":"UE5H","finish_reason":"SUCCESS"`+seed+`}`)
			defer server.Close()
			model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: stability.ModelCore}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := image.NewRequest("image")
			response, err := model.Call(t.Context(), request)
			if seed == `,"seed":-1` || seed == `,"seed":4294967295` {
				if err == nil {
					t.Fatal("invalid seed accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			value, present, err := response.First().Metadata.Decode[int64]("stability/seed")
			wantPresent := seed == `,"seed":0` || seed == `,"seed":42`
			if err != nil || present != wantPresent || seed == `,"seed":42` && value != 42 || seed == `,"seed":0` && value != 0 {
				t.Fatalf("seed=%d present=%t error=%v", value, present, err)
			}
		})
	}
}

func TestImageRequiresNativeVerdictAndProjectsOfficialRequestID(t *testing.T) {
	for _, finish := range []string{"", `,"finish_reason":""`, `,"finish_reason":null`, `,"finish_reason":"OTHER"`, `,"finish_reason":"CONTENT_FILTERED"`, `,"finish_reason":"SUCCESS"`} {
		t.Run(finish, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"image":"UE5H"`+finish+`}`)
			defer server.Close()
			model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: stability.ModelCore}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := image.NewRequest("image")
			response, err := model.Call(t.Context(), request)
			if finish == `,"finish_reason":"SUCCESS"` {
				if err != nil || response == nil {
					t.Fatalf("success=%v, %v", response, err)
				}
			} else if err == nil || response != nil {
				t.Fatalf("invalid verdict=%v, %v", response, err)
			}
		})
	}
	for _, header := range []string{"x-request-id", "request-id", ""} {
		t.Run(header, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if header != "" {
					w.Header().Set(header, "provider-id")
				}
				_, _ = w.Write([]byte(`{"image":"UE5H","finish_reason":"SUCCESS"}`))
			}))
			defer server.Close()
			model, err := stability.NewImageModel(t.Context(), stability.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: image.Options{Model: stability.ModelCore}})
			if err != nil {
				t.Fatal(err)
			}
			request, _ := image.NewRequest("image")
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			id, present, err := response.Metadata.Extra.Decode[string]("stability/request_id")
			if err != nil || present != (header == "x-request-id") || present && id != "provider-id" {
				t.Fatalf("request ID=%q, %t, %v", id, present, err)
			}
		})
	}
}
