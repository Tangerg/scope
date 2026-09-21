package blackforestlabs_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/models/blackforestlabs"
)

func TestImageRejectsNativeCoreFieldsBeforeIO(t *testing.T) {
	for _, field := range []string{"prompt", "width", "height", "seed", "output_format"} {
		for _, defaults := range []bool{false, true} {
			t.Run(field+map[bool]string{false: "/request", true: "/defaults"}[defaults], func(t *testing.T) {
				requests := 0
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
					requests++
					writer.WriteHeader(http.StatusBadRequest)
				}))
				defer server.Close()
				native := map[string]any{field: nil}
				if strings.HasPrefix(field, "config.") {
					native = map[string]any{"config": map[string]any{strings.TrimPrefix(field, "config."): nil}}
				}
				var extensions metadata.Extensions
				if err := extensions.Set(blackforestlabs.ImageRequestExtensionKey, native); err != nil {
					t.Fatal(err)
				}
				options := image.Options{Model: "flux-pro-1.1"}
				request, err := image.NewRequest("current prompt")
				if err != nil {
					t.Fatal(err)
				}
				if defaults {
					options.Extensions = extensions
				} else {
					request.Options.Extensions = extensions
				}
				model, err := blackforestlabs.NewImageModel(t.Context(), blackforestlabs.ImageModelConfig{APIKey: "test", BaseURL: server.URL, DefaultOptions: options})
				if err != nil {
					t.Fatal(err)
				}
				_, err = model.Call(t.Context(), request)
				if err == nil || !strings.Contains(err.Error(), "owned by Core") || requests != 0 {
					t.Fatalf("native field accepted: requests=%d err=%v", requests, err)
				}
			})
		}
	}
}
