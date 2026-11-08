package providerconformance_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/invocation"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/moderation"
	"github.com/Tangerg/scope/core/rerank"
	"github.com/Tangerg/scope/core/speech"
	"github.com/Tangerg/scope/core/transcription"
	"github.com/Tangerg/scope/models/anthropic"
	"github.com/Tangerg/scope/models/bedrock"
	"github.com/Tangerg/scope/models/cohere"
	"github.com/Tangerg/scope/models/deepgram"
	"github.com/Tangerg/scope/models/elevenlabs"
	"github.com/Tangerg/scope/models/hume"
	"github.com/Tangerg/scope/models/jina"
	"github.com/Tangerg/scope/models/mistral"
	"github.com/Tangerg/scope/models/nomic"
	"github.com/Tangerg/scope/models/ollama"
	"github.com/Tangerg/scope/models/openai"
	"github.com/Tangerg/scope/models/prodia"
	openaiprotocol "github.com/Tangerg/scope/models/protocol/openai"
	"github.com/Tangerg/scope/models/voyage"
)

type invocationBoundaryTransport func(*http.Request) (*http.Response, error)

func (i invocationBoundaryTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	return i(request)
}

type invocationBoundaryBody struct{ cause error }

func (i invocationBoundaryBody) Read([]byte) (int, error) { return 0, i.cause }
func (i invocationBoundaryBody) Close() error             { return nil }

type invocationBoundaryBinding struct {
	name   string
	retry  bool
	create func(*http.Client) func(bool) error
}

func invocationBoundaryBindings(t *testing.T) []invocationBoundaryBinding {
	t.Helper()
	audio, err := media.NewBytes("audio/wav", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	chatCall := func(model chat.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			if invalid {
				request = &chat.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	embeddingCall := func(model embedding.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &embedding.Request{Texts: []string{"text"}}
			if invalid {
				request = &embedding.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	imageCall := func(model image.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &image.Request{Prompt: "landscape"}
			if invalid {
				request = &image.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	moderationCall := func(model moderation.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &moderation.Request{Texts: []string{"text"}}
			if invalid {
				request = &moderation.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	rerankCall := func(model rerank.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &rerank.Request{Query: "query", Documents: []string{"document"}}
			if invalid {
				request = &rerank.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	speechCall := func(model speech.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &speech.Request{Text: "hello"}
			if invalid {
				request = &speech.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	streamingSpeechCall := func(model speech.Streamer, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &speech.Request{Text: "hello"}
			if invalid {
				request = &speech.Request{}
			}
			for _, err := range model.Stream(t.Context(), request) {
				if err != nil {
					return err
				}
			}
			return nil
		}
	}
	transcribeCall := func(model transcription.Model, constructionErr error) func(bool) error {
		if constructionErr != nil {
			t.Fatal(constructionErr)
		}
		return func(invalid bool) error {
			request := &transcription.Request{Audio: audio}
			if invalid {
				request = &transcription.Request{}
			}
			_, err := model.Call(t.Context(), request)
			return err
		}
	}
	cohereOptions := embedding.Options{Model: "embed-v4.0"}
	if err := cohereOptions.Extensions.Set(cohere.EmbeddingRequestExtensionKey, cohere.EmbeddingRequestOptions{InputType: "search_document"}); err != nil {
		t.Fatal(err)
	}
	return []invocationBoundaryBinding{
		{name: "openai-chat-completions", retry: true, create: func(client *http.Client) func(bool) error {
			return chatCall(openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt-5"}}))
		}},
		{name: "openai-responses", retry: true, create: func(client *http.Client) func(bool) error {
			return chatCall(openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt-5"}}))
		}},
		{name: "anthropic-messages", retry: true, create: func(client *http.Client) func(bool) error {
			return chatCall(anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "claude-sonnet-4-6"}}))
		}},
		{name: "bedrock-chat", retry: false, create: func(client *http.Client) func(bool) error {
			return chatCall(bedrock.NewChat(t.Context(), bedrock.ChatConfig{HTTPClient: client, DefaultOptions: chat.Options{Model: "amazon.nova-pro-v1:0"}, Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}}))
		}},
		{name: "mistral-chat", retry: false, create: func(client *http.Client) func(bool) error {
			return chatCall(mistral.NewChat(t.Context(), mistral.ChatConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "mistral-large-latest"}}))
		}},
		{name: "ollama-chat", retry: false, create: func(client *http.Client) func(bool) error {
			return chatCall(ollama.NewChat(t.Context(), ollama.ChatConfig{HTTPClient: client, DefaultOptions: chat.Options{Model: "llama3.2"}}))
		}},
		{name: "openai-embedding", retry: true, create: func(client *http.Client) func(bool) error {
			return embeddingCall(openai.NewEmbeddingModel(t.Context(), openai.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: embedding.Options{Model: "text-embedding-3-small"}}))
		}},
		{name: "mistral-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(mistral.NewEmbeddingModel(t.Context(), mistral.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: embedding.Options{Model: "mistral-embed"}}))
		}},
		{name: "ollama-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(ollama.NewEmbeddingModel(t.Context(), ollama.EmbeddingModelConfig{HTTPClient: client, DefaultOptions: embedding.Options{Model: "nomic-embed-text"}}))
		}},
		{name: "jina-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(jina.NewEmbeddingModel(t.Context(), jina.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: embedding.Options{Model: "jina-embeddings-v3"}}))
		}},
		{name: "voyage-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(voyage.NewEmbeddingModel(t.Context(), voyage.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: embedding.Options{Model: "voyage-3"}}))
		}},
		{name: "nomic-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(nomic.NewEmbeddingModel(t.Context(), nomic.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: embedding.Options{Model: "nomic-embed-text-v1.5"}}))
		}},
		{name: "cohere-embedding", retry: true, create: func(client *http.Client) func(bool) error {
			return embeddingCall(cohere.NewEmbeddingModel(t.Context(), cohere.EmbeddingModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: cohereOptions}))
		}},
		{name: "bedrock-embedding", retry: false, create: func(client *http.Client) func(bool) error {
			return embeddingCall(bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{HTTPClient: client, DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"}, Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}}))
		}},
		{name: "cohere-rerank", retry: true, create: func(client *http.Client) func(bool) error {
			return rerankCall(cohere.NewRerankModel(t.Context(), cohere.RerankModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: rerank.Options{Model: "rerank-v3.5"}}))
		}},
		{name: "jina-rerank", retry: false, create: func(client *http.Client) func(bool) error {
			return rerankCall(jina.NewRerankModel(t.Context(), jina.RerankModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: rerank.Options{Model: "jina-reranker-v2-base-multilingual"}}))
		}},
		{name: "voyage-rerank", retry: false, create: func(client *http.Client) func(bool) error {
			return rerankCall(voyage.NewRerankModel(t.Context(), voyage.RerankModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: rerank.Options{Model: "rerank-2"}}))
		}},
		{name: "openai-moderation", retry: true, create: func(client *http.Client) func(bool) error {
			return moderationCall(openai.NewModerationModel(t.Context(), openai.ModerationModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: moderation.Options{Model: "omni-moderation-latest"}}))
		}},
		{name: "mistral-moderation", retry: false, create: func(client *http.Client) func(bool) error {
			return moderationCall(mistral.NewModerationModel(t.Context(), mistral.ModerationModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: moderation.Options{Model: "mistral-moderation-latest"}}))
		}},
		{name: "openai-transcription", retry: true, create: func(client *http.Client) func(bool) error {
			return transcribeCall(openai.NewTranscriptionModel(t.Context(), openai.TranscriptionModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: transcription.Options{Model: "whisper-1"}}))
		}},
		{name: "deepgram-transcription", retry: false, create: func(client *http.Client) func(bool) error {
			return transcribeCall(deepgram.NewTranscriptionModel(t.Context(), deepgram.TranscriptionModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: transcription.Options{Model: "nova-3"}}))
		}},
		{name: "elevenlabs-transcription", retry: false, create: func(client *http.Client) func(bool) error {
			return transcribeCall(elevenlabs.NewTranscriptionModel(t.Context(), elevenlabs.TranscriptionModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: transcription.Options{Model: "scribe_v1"}}))
		}},
		{name: "openai-audio-translation", retry: true, create: func(client *http.Client) func(bool) error {
			return transcribeCall(openaiprotocol.NewAudioTranslationModel(t.Context(), openaiprotocol.AudioTranslationModelConfig{Provider: openai.Provider, APIKey: "test", HTTPClient: client, DefaultOptions: transcription.Options{Model: "whisper-1"}}))
		}},
		{name: "openai-image", retry: true, create: func(client *http.Client) func(bool) error {
			return imageCall(openai.NewImageModel(t.Context(), openai.ImageModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: image.Options{Model: "gpt-image-1"}}))
		}},
		{name: "prodia-image", retry: false, create: func(client *http.Client) func(bool) error {
			return imageCall(prodia.NewImageModel(t.Context(), prodia.ImageModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: image.Options{Model: "inference.flux-2.dev.txt2img.v1"}}))
		}},
		{name: "openai-speech", retry: true, create: func(client *http.Client) func(bool) error {
			return speechCall(openai.NewSpeechModel(t.Context(), openai.SpeechModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: speech.Options{Model: "gpt-4o-mini-tts", Voice: "alloy"}}))
		}},
		{name: "deepgram-speech", retry: false, create: func(client *http.Client) func(bool) error {
			return speechCall(deepgram.NewSpeechModel(t.Context(), deepgram.SpeechModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: speech.Options{Model: "aura-2-thalia-en", Voice: ""}}))
		}},
		{name: "elevenlabs-speech", retry: false, create: func(client *http.Client) func(bool) error {
			return speechCall(elevenlabs.NewSpeechModel(t.Context(), elevenlabs.SpeechModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: speech.Options{Model: "eleven_multilingual_v2", Voice: "voice"}}))
		}},
		{name: "hume-speech", retry: false, create: func(client *http.Client) func(bool) error {
			return speechCall(hume.NewSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: speech.Options{Model: "1", Voice: "voice"}}))
		}},
		{name: "hume-streaming-speech", retry: false, create: func(client *http.Client) func(bool) error {
			return streamingSpeechCall(hume.NewStreamingSpeechModel(t.Context(), hume.SpeechModelConfig{APIKey: "test", HTTPClient: client, DefaultOptions: speech.Options{Model: hume.ModelOctave1, Voice: "voice"}}))
		}},
	}
}

func TestInvocationBoundaryOwnsExecutionEvidence(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	for _, binding := range invocationBoundaryBindings(t) {
		t.Run(binding.name, func(t *testing.T) {
			for _, phase := range []string{"local admission", "transport failure", "response body failure", "ordinary body failure", "HTTP error body failure"} {
				t.Run(phase, func(t *testing.T) {
					attempts := 0
					sentinel := errors.New("nested operation refused")
					cause := error(&invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: sentinel})
					if phase == "ordinary body failure" {
						sentinel = io.ErrUnexpectedEOF
						cause = sentinel
					}
					call := binding.create(&http.Client{Transport: invocationBoundaryTransport(func(request *http.Request) (*http.Response, error) {
						attempts++
						if phase == "local admission" || phase == "transport failure" {
							return nil, cause
						}
						status := http.StatusOK
						if phase == "HTTP error body failure" {
							status = http.StatusBadRequest
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": {"application/json"}}, Body: invocationBoundaryBody{cause: cause}, Request: request}, nil
					})})
					err := call(phase == "local admission")
					expected := invocation.FailureUnknown
					if phase == "local admission" {
						expected = invocation.FailureNotExecuted
						if attempts != 0 {
							t.Fatalf("invalid input reached provider %d times", attempts)
						}
					} else if attempts == 0 {
						t.Fatal("valid input never reached external execution")
					}
					if err == nil || invocation.ClassifyFailure(err) != expected {
						t.Fatalf("attempts=%d classification=%s error=%v; want %s", attempts, invocation.ClassifyFailure(err), err, expected)
					}
					if (phase == "transport failure" || phase == "response body failure") && !errors.Is(err, sentinel) {
						t.Fatalf("cause was lost: %v", err)
					}
				})
			}
		})
	}
}

func TestSDKRetryCannotBorrowLastAttemptEvidence(t *testing.T) {
	for _, binding := range invocationBoundaryBindings(t) {
		if !binding.retry {
			continue
		}
		t.Run(binding.name, func(t *testing.T) {
			attempts := 0
			cause := errors.New("host refused later SDK attempt")
			call := binding.create(&http.Client{Transport: invocationBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{"Content-Type": {"application/json"}, "Retry-After-Ms": {"1"}, "Retry-After": {"0"}}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"first provider outcome unknown","type":"server_error"},"message":"first provider outcome unknown"}`)), Request: request}, nil
				}
				return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: cause}
			})})
			err := call(false)
			if attempts < 2 || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, cause) {
				t.Fatalf("attempts=%d classification=%s error=%v", attempts, invocation.ClassifyFailure(err), err)
			}
		})
	}
}

func TestTitanBatchFailureOwnsEarlierExecution(t *testing.T) {
	t.Setenv("AWS_MAX_ATTEMPTS", "1")
	for _, modelID := range []string{"amazon.titan-embed-text-v1", "amazon.titan-embed-text-v2:0"} {
		t.Run(modelID, func(t *testing.T) {
			attempts := 0
			cause := errors.New("second native invocation refused")
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: embedding.Options{Model: modelID}, HTTPClient: &http.Client{Transport: invocationBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				attempts++
				if attempts == 1 {
					return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{"embedding":[0.1,0.2],"inputTextTokenCount":3}`)), Request: request}, nil
				}
				return nil, &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: cause}
			})}})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"first", "second"}})
			if response != nil || attempts != 2 || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, cause) {
				t.Fatalf("response=%v attempts=%d error=%v", response, attempts, err)
			}
		})
	}
}

func TestBedrockEmbeddingAdmissionPrecedesEveryNativeInvocation(t *testing.T) {
	for _, test := range []struct {
		name, model string
		texts       []string
		dimensions  *int64
		native      bedrock.EmbeddingRequestOptions
	}{
		{name: "Titan V1 dimensions", model: "amazon.titan-embed-text-v1", dimensions: new(int64(256))},
		{name: "Titan V1 normalization", model: "amazon.titan-embed-text-v1", native: bedrock.EmbeddingRequestOptions{Normalize: new(true)}},
		{name: "Titan V2 dimensions", model: "amazon.titan-embed-text-v2:0", dimensions: new(int64(768))},
		{name: "Titan V2 Cohere options", model: "amazon.titan-embed-text-v2:0", native: bedrock.EmbeddingRequestOptions{InputType: "search_document"}},
		{name: "Titan invalid later text", model: "amazon.titan-embed-text-v2:0", texts: []string{"valid", string([]byte{0xff})}},
		{name: "Cohere V3 input type", model: "cohere.embed-english-v3"},
		{name: "Cohere V3 dimensions", model: "cohere.embed-english-v3", dimensions: new(int64(256)), native: bedrock.EmbeddingRequestOptions{InputType: "search_document"}},
		{name: "Cohere V3 truncate", model: "cohere.embed-english-v3", native: bedrock.EmbeddingRequestOptions{InputType: "search_document", Truncate: "LEFT"}},
		{name: "Cohere V4 dimensions", model: "cohere.embed-v4:0", dimensions: new(int64(768)), native: bedrock.EmbeddingRequestOptions{InputType: "search_document"}},
		{name: "Cohere V4 normalize", model: "cohere.embed-v4:0", native: bedrock.EmbeddingRequestOptions{InputType: "search_document", Normalize: new(true)}},
		{name: "Cohere V4 truncate", model: "cohere.embed-v4:0", native: bedrock.EmbeddingRequestOptions{InputType: "search_document", Truncate: "START"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			attempts := 0
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: embedding.Options{Model: test.model}, HTTPClient: &http.Client{Transport: invocationBoundaryTransport(func(*http.Request) (*http.Response, error) {
				attempts++
				return nil, errors.New("unexpected provider request")
			})}})
			if err != nil {
				t.Fatal(err)
			}
			texts := test.texts
			if texts == nil {
				texts = []string{"first", "second"}
			}
			request := &embedding.Request{Texts: texts, Options: embedding.Options{Dimensions: test.dimensions}}
			if err = request.Options.Extensions.Set(bedrock.EmbeddingRequestExtensionKey, test.native); err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || attempts != 0 || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted {
				t.Fatalf("response=%v attempts=%d error=%v", response, attempts, err)
			}
		})
	}
}

func TestGeneratedChatTextCannotBorrowLaterReadFailureEvidence(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		name := "Call"
		if streaming {
			name = "Stream"
		}
		t.Run(name, func(t *testing.T) {
			sentinel := errors.New("later body operation refused")
			client := &http.Client{Transport: invocationBoundaryTransport(func(request *http.Request) (*http.Response, error) {
				body := io.NopCloser(io.MultiReader(strings.NewReader("data: {\"id\":\"response\",\"model\":\"test-model\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"already generated\"}}]}\n\n"), invocationBoundaryBody{cause: &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: sentinel}}))
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: body, Request: request}, nil
			})}
			model, err := openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "test-model"}})
			if err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello"))}}
			if !streaming {
				response, err := model.Call(t.Context(), request)
				if response != nil || invocation.ClassifyFailure(err) != invocation.FailureUnknown || !errors.Is(err, sentinel) {
					t.Fatalf("response=%v error=%v", response, err)
				}
				return
			}
			text := ""
			var failure error
			for delta, err := range model.Stream(t.Context(), request) {
				if err != nil {
					failure = err
					break
				}
				for _, part := range delta.Parts {
					text += part.Text
				}
			}
			if text != "already generated" || invocation.ClassifyFailure(failure) != invocation.FailureUnknown || !errors.Is(failure, sentinel) {
				t.Fatalf("text=%q error=%v", text, failure)
			}
		})
	}
}

func TestForeignMetadataOnlyAssistantCannotDisappearFromReplay(t *testing.T) {
	for _, binding := range []struct {
		name   string
		create func(*http.Client) (chat.Model, error)
	}{
		{"openai-chat-completions", func(client *http.Client) (chat.Model, error) {
			return openai.NewChatCompletions(t.Context(), openai.ChatCompletionsConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt-5"}})
		}},
		{"openai-responses", func(client *http.Client) (chat.Model, error) {
			return openai.NewResponses(t.Context(), openai.ResponsesConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "gpt-5"}})
		}},
		{"anthropic-messages", func(client *http.Client) (chat.Model, error) {
			return anthropic.NewMessages(t.Context(), anthropic.MessagesConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "claude-sonnet-4-6"}})
		}},
		{"bedrock-chat", func(client *http.Client) (chat.Model, error) {
			return bedrock.NewChat(t.Context(), bedrock.ChatConfig{HTTPClient: client, Region: "us-east-1", Credentials: &bedrock.Credentials{AccessKeyID: "test", SecretAccessKey: "test"}, DefaultOptions: chat.Options{Model: "amazon.nova-pro-v1:0"}})
		}},
		{"mistral-chat", func(client *http.Client) (chat.Model, error) {
			return mistral.NewChat(t.Context(), mistral.ChatConfig{APIKey: "test", HTTPClient: client, DefaultOptions: chat.Options{Model: "mistral-large-latest"}})
		}},
		{"ollama-chat", func(client *http.Client) (chat.Model, error) {
			return ollama.NewChat(t.Context(), ollama.ChatConfig{HTTPClient: client, DefaultOptions: chat.Options{Model: "llama3.2"}})
		}},
	} {
		t.Run(binding.name, func(t *testing.T) {
			attempts := 0
			model, err := binding.create(&http.Client{Transport: invocationBoundaryTransport(func(*http.Request) (*http.Response, error) {
				attempts++
				return nil, errors.New("unexpected provider request")
			})})
			if err != nil {
				t.Fatal(err)
			}
			assistant := chat.NewAssistantMessage()
			if err = assistant.Metadata.Set("foreign/native_replay", true); err != nil {
				t.Fatal(err)
			}
			request := &chat.Request{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("hello")), assistant}}
			if err = request.Validate(); err != nil {
				t.Fatalf("Core must admit provider-owned replay state: %v", err)
			}
			response, err := model.Call(t.Context(), request)
			if response != nil || invocation.ClassifyFailure(err) != invocation.FailureNotExecuted || attempts != 0 {
				t.Fatalf("response=%v error=%v attempts=%d", response, err, attempts)
			}
		})
	}
}
