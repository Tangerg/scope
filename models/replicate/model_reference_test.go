package replicate_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/models/replicate"
)

func TestBindingsRejectMalformedModelReferences(t *testing.T) {
	for _, reference := range []string{
		"owner/model?input=changed", "owner/model#fragment", "../model", "owner/..", "owner/model/other",
		"owner/model%2Fother", "owner/model version", "owner/model:short", "owner/model:", "owner/model:../../version",
	} {
		t.Run(reference, func(t *testing.T) {
			_, imageErr := replicate.NewImageModel(t.Context(), replicate.ImageModelConfig{
				APIKey: "test", DefaultOptions: image.Options{Model: reference}, InputSchema: replicate.FluxSchnellImageInputSchema(),
			})
			_, speechErr := replicate.NewSpeechModel(t.Context(), replicate.SpeechModelConfig{
				APIKey: "test", DefaultOptions: speech.Options{Model: reference}, InputSchema: replicate.XTTSV2SpeechInputSchema(),
			})
			if imageErr == nil || speechErr == nil {
				t.Fatalf("image=%v speech=%v", imageErr, speechErr)
			}
		})
	}
}

func TestSubmissionOwnsCompleteFailureEvidence(t *testing.T) {
	for _, status := range []string{"failed", "canceled", "aborted"} {
		t.Run(status, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"id":"prediction","status":"` + status + `","error":"provider verdict"}`))
			}))
			t.Cleanup(server.Close)
			model := newAbortTestModel(t, server)
			_, err := model.Call(t.Context(), &image.Request{Prompt: "tree"})
			if invocation.ClassifyFailure(err) != invocation.FailureFailed || !strings.Contains(err.Error(), "provider verdict") || calls != 1 {
				t.Fatalf("submission verdict error=%v calls=%d", err, calls)
			}
		})
	}
}

func TestCanceledBeforeSubmissionDoesNotExecute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	t.Cleanup(server.Close)
	model := newAbortTestModel(t, server)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := model.Call(ctx, &image.Request{Prompt: "tree"})
	if invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestSpeechWebhookPreflightDoesNotExecute(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(http.StatusBadRequest) }))
	t.Cleanup(server.Close)
	model, err := replicate.NewSpeechModel(t.Context(), replicate.SpeechModelConfig{
		APIKey: "test", BaseURL: server.URL, DefaultOptions: speech.Options{Model: "owner/model"},
		InputSchema: replicate.SpeechInputSchema{TextKey: "text", OutputKind: replicate.FileOutputURI},
	})
	if err != nil {
		t.Fatal(err)
	}
	request := &speech.Request{Text: "hello"}
	if setErr := request.Options.Extensions.Set(replicate.SpeechRequestExtensionKey, json.RawMessage(`{"webhook":"http://example.com/webhook"}`)); setErr != nil {
		t.Fatal(setErr)
	}
	_, err = model.Call(t.Context(), request)
	if invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || calls != 0 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}
