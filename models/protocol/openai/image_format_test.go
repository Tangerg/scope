package openai_test

import (
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/modeltest"
)

func TestImageResponseUsesReportedOutputFormat(t *testing.T) {
	for _, test := range []struct {
		name, requested, reported, want string
	}{
		{name: "reported JPEG", reported: "jpeg", want: "image/jpeg"},
		{name: "reported WebP", reported: "webp", want: "image/webp"},
		{name: "reported PNG", reported: "png", want: "image/png"},
		{name: "provider reports actual format", requested: "image/png", reported: "jpeg", want: "image/jpeg"},
		{name: "request supplies unreported format", requested: "image/webp", want: "image/webp"},
		{name: "provider default", want: "image/png"},
	} {
		t.Run(test.name, func(t *testing.T) {
			formatField := ""
			if test.reported != "" {
				formatField = fmt.Sprintf(`,"output_format":%q`, test.reported)
			}
			server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}]`+formatField+`}`)
			t.Cleanup(server.Close)
			model := newImageModel(t, server.URL, "image-model")
			request := &image.Request{Prompt: "an image", Options: image.Options{OutputFormat: media.Type(test.requested)}}
			response, err := model.Call(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			if response.First().Media.MIME != test.want {
				t.Fatalf("MIME=%q; want %q", response.First().Media.MIME, test.want)
			}
		})
	}
}

func TestImageRejectsUnknownReportedFormat(t *testing.T) {
	for _, format := range []string{`"unknown"`, `""`, `true`, `42`, `{}`, `[]`} {
		t.Run(format, func(t *testing.T) {
			server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}],"output_format":`+format+`}`)
			t.Cleanup(server.Close)
			model := newImageModel(t, server.URL, "image-model")
			response, err := model.Call(t.Context(), &image.Request{Prompt: "an image"})
			if response != nil || !errors.Is(err, image.ErrInvalidResponse) {
				t.Fatalf("response=%v error=%v", response, err)
			}
		})
	}
}

func TestImageRejectsUnsupportedRequestFormatBeforeIO(t *testing.T) {
	calls := 0
	server := modeltest.JSONServer(http.StatusOK, `{"data":[{"b64_json":"aW1hZ2U="}]}`, func(request *http.Request) { calls++ })
	t.Cleanup(server.Close)
	model := newImageModel(t, server.URL, "image-model")
	response, err := model.Call(t.Context(), &image.Request{Prompt: "an image", Options: image.Options{OutputFormat: "image/gif"}})
	if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls != 0 {
		t.Fatalf("response=%v error=%v transport calls=%d", response, err, calls)
	}
}
