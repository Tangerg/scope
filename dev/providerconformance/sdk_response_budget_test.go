package providerconformance_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/anthropic"
	"github.com/Tangerg/scope/models/cohere"
	"github.com/Tangerg/scope/models/google"
	"github.com/Tangerg/scope/models/google/vertexai"
	"github.com/Tangerg/scope/models/hume"
	"github.com/Tangerg/scope/models/luma"
	"github.com/Tangerg/scope/models/openai"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
)

type responseBudgetBody struct {
	reader   io.Reader
	read     int64
	closed   int
	closeErr error
}

func (r *responseBudgetBody) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.read += int64(count)
	return count, err
}

func (r *responseBudgetBody) Close() error { r.closed++; return r.closeErr }

type responseBudgetTransport struct {
	reader      func(*http.Request) io.Reader
	status      int
	contentType string
	closeErr    error
	bodies      []*responseBudgetBody
}

func (r *responseBudgetTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		_ = request.Body.Close()
	}
	body := &responseBudgetBody{reader: r.reader(request), closeErr: r.closeErr}
	r.bodies = append(r.bodies, body)
	return &http.Response{StatusCode: r.status, Header: http.Header{"Content-Type": {r.contentType}}, Body: body, ContentLength: 1, Request: request}, nil
}

type repeatedResponseByte byte

func (r repeatedResponseByte) Read(buffer []byte) (int, error) {
	for index := range buffer {
		buffer[index] = byte(r)
	}
	return len(buffer), nil
}

type responseBudgetCall struct {
	name     string
	attempts int
	call     func(context.Context, *http.Client) error
}

const responseBudgetEndpoint = "https://api.scope.test"

func boundedJSONCalls() []responseBudgetCall {
	return []responseBudgetCall{
		{name: "google token count", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := google.NewTextCounter(ctx, google.TextCounterConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, Model: "gemini"})
			if err != nil {
				return err
			}
			_, err = model.CountText(ctx, "hello")
			return err
		}},
		{name: "google embedding", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := google.NewEmbeddingModel(ctx, google.EmbeddingModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "gemini-embedding-2"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "vertex embedding", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := vertexai.NewEmbeddingModel(ctx, vertexai.EmbeddingModelConfig{Client: vertexai.ClientConfig{Project: "project", Location: "global", BaseURL: responseBudgetEndpoint, HTTPClient: client}, DefaultOptions: embedding.Options{Model: "text-embedding-005"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "cohere embedding", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := cohere.NewEmbeddingModel(ctx, cohere.EmbeddingModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "embed-v4.0"}})
			if err != nil {
				return err
			}
			request := &embedding.Request{Texts: []string{"hello"}}
			if extensionErr := request.Options.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document"}); extensionErr != nil {
				return extensionErr
			}
			_, err = model.Call(ctx, request)
			return err
		}},
		{name: "cohere rerank", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := cohere.NewRerankModel(ctx, cohere.RerankModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: rerank.Options{Model: "rerank-v3.5"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &rerank.Request{Query: "hello", Documents: []string{"hello"}})
			return err
		}},
		{name: "openai embedding", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewEmbeddingModel(ctx, openai.EmbeddingModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: embedding.Options{Model: "text-embedding-3-small"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &embedding.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "openai image", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewImageModel(ctx, openai.ImageModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: image.Options{Model: "gpt-image-1"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "hello"})
			return err
		}},
		{name: "openai moderation", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewModerationModel(ctx, openai.ModerationModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: moderation.Options{Model: "omni-moderation-latest"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &moderation.Request{Texts: []string{"hello"}})
			return err
		}},
		{name: "openai transcription", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewTranscriptionModel(ctx, openai.TranscriptionModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: "whisper-1"}})
			if err != nil {
				return err
			}
			audio, err := media.NewBytes("audio/wav", []byte("audio"))
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &transcription.Request{Audio: audio})
			return err
		}},
		{name: "openai translation", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openaiprotocol.NewAudioTranslationModel(ctx, openaiprotocol.AudioTranslationModelConfig{Provider: openai.Provider, APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: transcription.Options{Model: "whisper-1"}})
			if err != nil {
				return err
			}
			audio, err := media.NewBytes("audio/wav", []byte("audio"))
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &transcription.Request{Audio: audio})
			return err
		}},
		{name: "openai token count", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewResponses(ctx, openai.ResponsesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt"}})
			if err != nil {
				return err
			}
			_, err = model.CountInputTokens(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "anthropic token count", attempts: 3, call: func(ctx context.Context, client *http.Client) error {
			model, err := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "claude"}})
			if err != nil {
				return err
			}
			_, err = model.CountInputTokens(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "luma create", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := luma.NewImageModel(ctx, luma.ImageModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, OutputOrigins: []string{responseBudgetEndpoint}, DefaultOptions: image.Options{Model: luma.ModelUni1}, Polling: invocation.Polling{Interval: time.Nanosecond}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &image.Request{Prompt: "hello"})
			return err
		}},
	}
}

func TestSDKJSONResponsesAreBoundedBeforeDecoding(t *testing.T) {
	const limit = httpclient.DefaultMaxJSONResponseBytes
	for _, sample := range boundedJSONCalls() {
		for _, status := range []int{http.StatusOK, http.StatusBadRequest} {
			t.Run(sample.name+"/"+http.StatusText(status), func(t *testing.T) {
				transport := &responseBudgetTransport{status: status, contentType: "application/json", reader: func(*http.Request) io.Reader {
					return io.MultiReader(io.LimitReader(repeatedResponseByte(' '), limit+1), strings.NewReader(`{"value":"not reached"}`))
				}}
				err := sample.call(t.Context(), &http.Client{Transport: transport})
				limitErr, limited := errors.AsType[*http.MaxBytesError](err)
				if !limited || limitErr.Limit != limit || len(transport.bodies) != sample.attempts || invocation.ClassifyFailure(err) != invocation.FailureUnknown {
					t.Fatalf("error=%v calls=%d", err, len(transport.bodies))
				}
				for _, body := range transport.bodies {
					if body.read != limit+1 || body.closed == 0 {
						t.Fatalf("bytes=%d closed=%d", body.read, body.closed)
					}
				}
			})
		}
	}
}

func TestLumaPollMetadataUsesTheSameJSONBudget(t *testing.T) {
	const limit = httpclient.DefaultMaxJSONResponseBytes
	transport := &responseBudgetTransport{status: http.StatusOK, contentType: "application/json", reader: func(request *http.Request) io.Reader {
		if request.Method == http.MethodPost {
			return strings.NewReader(`{"id":"job","state":"queued"}`)
		}
		return io.MultiReader(io.LimitReader(repeatedResponseByte(' '), limit+1), strings.NewReader(`{"id":"job","state":"completed"}`))
	}}
	for _, sample := range boundedJSONCalls() {
		if sample.name != "luma create" {
			continue
		}
		err := sample.call(t.Context(), &http.Client{Transport: transport})
		if _, limited := errors.AsType[*http.MaxBytesError](err); !limited || len(transport.bodies) != 2 {
			t.Fatalf("error=%v calls=%d", err, len(transport.bodies))
		}
		if transport.bodies[1].read != limit+1 || transport.bodies[0].closed == 0 || transport.bodies[1].closed == 0 {
			t.Fatalf("bodies=%+v", transport.bodies)
		}
	}
}

func sdkStreamingCalls() []responseBudgetCall {
	return []responseBudgetCall{
		{name: "openai chat", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewChatCompletions(ctx, openai.ChatCompletionsConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "openai responses", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewResponses(ctx, openai.ResponsesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "anthropic messages", attempts: 3, call: func(ctx context.Context, client *http.Client) error {
			model, err := anthropic.NewMessages(ctx, anthropic.MessagesConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: chat.Options{Model: "claude"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
			return err
		}},
		{name: "openai speech", attempts: 1, call: func(ctx context.Context, client *http.Client) error {
			model, err := openai.NewSpeechModel(ctx, openai.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: client, DefaultOptions: speech.Options{Model: "tts-1", Voice: "alloy"}})
			if err != nil {
				return err
			}
			_, err = model.Call(ctx, &speech.Request{Text: "hello"})
			return err
		}},
	}
}

func TestSDKStreamingErrorEnvelopesAreBounded(t *testing.T) {
	for _, sample := range sdkStreamingCalls() {
		t.Run(sample.name, func(t *testing.T) {
			transport := &responseBudgetTransport{status: http.StatusBadRequest, contentType: "application/json", reader: func(*http.Request) io.Reader {
				return io.LimitReader(repeatedResponseByte(' '), httpclient.DefaultMaxJSONResponseBytes+2)
			}}
			err := sample.call(t.Context(), &http.Client{Transport: transport})
			if _, limited := errors.AsType[*http.MaxBytesError](err); !limited || len(transport.bodies) != sample.attempts {
				t.Fatalf("error=%v calls=%d", err, len(transport.bodies))
			}
			for _, body := range transport.bodies {
				if body.read != httpclient.DefaultMaxJSONResponseBytes+1 || body.closed == 0 {
					t.Fatalf("bytes=%d closed=%d", body.read, body.closed)
				}
			}
		})
	}
}

func TestSDKStreamingBudgetBoundsTheWholeMultilineEvent(t *testing.T) {
	const linePadding = httpclient.DefaultMaxJSONResponseBytes / 2
	for _, sample := range sdkStreamingCalls() {
		if sample.name == "openai speech" {
			continue
		}
		t.Run(sample.name, func(t *testing.T) {
			transport := &responseBudgetTransport{status: http.StatusOK, contentType: "text/event-stream", reader: func(*http.Request) io.Reader {
				return io.MultiReader(strings.NewReader("data: {"), io.LimitReader(repeatedResponseByte(' '), linePadding), strings.NewReader("\ndata: "), io.LimitReader(repeatedResponseByte(' '), linePadding), strings.NewReader("\ndata: }\n\n"))
			}}
			err := sample.call(t.Context(), &http.Client{Transport: transport})
			if _, limited := errors.AsType[*http.MaxBytesError](err); !limited || len(transport.bodies) != 1 {
				t.Fatalf("error=%v calls=%d", err, len(transport.bodies))
			}
			if body := transport.bodies[0]; body.read != httpclient.DefaultMaxJSONResponseBytes+1 || body.closed == 0 {
				t.Fatalf("bytes=%d closed=%d", body.read, body.closed)
			}
		})
	}
}

func TestSSEBudgetResetsBetweenWireEvents(t *testing.T) {
	const padding = httpclient.DefaultMaxJSONResponseBytes / 2
	transport := &responseBudgetTransport{status: http.StatusOK, contentType: "text/event-stream", reader: func(*http.Request) io.Reader {
		return io.MultiReader(
			strings.NewReader(":"), io.LimitReader(repeatedResponseByte(' '), padding), strings.NewReader("\n\n"),
			strings.NewReader(":"), io.LimitReader(repeatedResponseByte(' '), padding), strings.NewReader("\n\n"),
			strings.NewReader("data: {\"id\":\"m\",\"model\":\"gpt\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"answer\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"),
		)
	}}
	model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: &http.Client{Transport: transport}, DefaultOptions: chat.Options{Model: "gpt"}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := model.Call(t.Context(), &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}})
	if err != nil || response == nil || len(transport.bodies) != 1 {
		t.Fatalf("response=%v error=%v calls=%d", response, err, len(transport.bodies))
	}
	if body := transport.bodies[0]; body.read <= httpclient.DefaultMaxJSONResponseBytes || body.closed == 0 {
		t.Fatalf("bytes=%d closed=%d", body.read, body.closed)
	}
}

func TestHumeRejectsAnOversizedNDJSONEventBeforeBase64Decoding(t *testing.T) {
	transport := &responseBudgetTransport{status: http.StatusOK, contentType: "application/x-ndjson", reader: func(*http.Request) io.Reader {
		return io.MultiReader(strings.NewReader(`{"type":"audio","audio":"`), io.LimitReader(repeatedResponseByte('A'), httpclient.DefaultMaxJSONResponseBytes+4), strings.NewReader(`","audio_format":"mp3","generation_id":"g","request_id":"r","snippet_id":"s","chunk_index":0,"is_last_chunk":true}`))
	}}
	model, err := hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", BaseURL: responseBudgetEndpoint, HTTPClient: &http.Client{Transport: transport}, DefaultOptions: speech.Options{Model: hume.ModelOctave2, Voice: "voice"}})
	if err != nil {
		t.Fatal(err)
	}
	chunks, failures := 0, 0
	for response, err := range model.Stream(t.Context(), &speech.Request{Text: "hello"}) {
		if response != nil {
			chunks++
		}
		if err != nil {
			failures++
		}
	}
	if chunks != 0 || failures != 1 || len(transport.bodies) != 1 {
		t.Fatalf("chunks=%d failures=%d calls=%d", chunks, failures, len(transport.bodies))
	}
	if body := transport.bodies[0]; body.read > httpclient.DefaultMaxJSONResponseBytes+2 || body.closed != 1 {
		t.Fatalf("bytes=%d closed=%d", body.read, body.closed)
	}
}
