package protocol

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"reflect"

	"github.com/Tangerg/sse"
	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/invocation"
)

const (
	maxGenerationEventBytes  = 256 << 20
	headerAuthorization      = "Authorization"
	headerProxyAuthorization = "Proxy-Authorization"
	headerGoogleAPIKey       = "X-Goog-Api-Key"
	headerAPIKey             = "Api-Key"
	headerXAPIKey            = "X-Api-Key"
	headerCookie             = "Cookie"
)

var ErrInvalidGenerationResponse = errors.New("google: invalid generation response")

// GenerationClient owns GenerateContent's native request and response boundary.
// The SDK owns authentication and complete request projection. Scope owns
// strict response decoding and the lifetime of the single real HTTP request.
type GenerationClient struct{ config genai.ClientConfig }

func NewGenerationClient(ctx context.Context, config ClientConfig) (*GenerationClient, error) {
	api, err := newAPI(ctx, config)
	if err != nil {
		return nil, err
	}
	return api.generation, nil
}

func (g *GenerationClient) GenerateContent(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) (*genai.GenerateContentResponse, error) {
	response, cancel, err := g.request(ctx, model, contents, config, false)
	if err != nil {
		return nil, err
	}
	defer cancel()
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxGenerationEventBytes+1))
	if err != nil {
		return nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: err}
	}
	if len(raw) > maxGenerationEventBytes {
		return nil, fmt.Errorf("%w: response exceeds byte limit", ErrInvalidGenerationResponse)
	}
	return g.decode(raw, response.Header)
}

func (g *GenerationClient) GenerateContentStream(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig) iter.Seq2[*genai.GenerateContentResponse, error] {
	return func(yield func(*genai.GenerateContentResponse, error) bool) {
		response, cancel, err := g.request(ctx, model, contents, config, true)
		if err != nil {
			yield(nil, err)
			return
		}
		defer cancel()
		defer response.Body.Close()
		events := sse.NewReader(&generationStreamBody{
			reader: response.Body, client: g, headers: response.Header, lineStart: true,
		})
		events.MaxLineBytes = maxGenerationEventBytes
		events.MaxEventBytes = maxGenerationEventBytes
		for event, eventErr := range events.Messages() {
			if eventErr != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: eventErr})
				return
			}
			chunk, decodeErr := g.decode(event.Data, response.Header)
			if decodeErr != nil {
				yield(nil, &invocation.Failure{Kind: invocation.FailureUnknown, Cause: decodeErr})
				return
			}
			if !yield(chunk, nil) {
				return
			}
		}
	}
}

func (g *GenerationClient) request(ctx context.Context, model string, contents []*genai.Content, config *genai.GenerateContentConfig, streaming bool) (_ *http.Response, _ context.CancelFunc, err error) {
	dispatched := false
	defer func() {
		if err != nil {
			kind := invocation.FailureNotExecuted
			if dispatched {
				kind = invocation.FailureUnknown
			}
			err = &invocation.Failure{Kind: kind, Cause: err}
		}
	}()
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}
	if config.AutomaticContinuation != nil && *config.AutomaticContinuation {
		return nil, nil, errors.New("google: automatic continuation is unsupported; each call owns one native generation")
	}
	if validationErr := validateGenerationHTTPOptions(config.HTTPOptions); validationErr != nil {
		return nil, nil, validationErr
	}
	requestConfig := *config
	headers, err := g.requestHeaders(config.HTTPOptions)
	if err != nil {
		return nil, nil, err
	}
	options := genai.HTTPOptions{Headers: headers}
	if config.HTTPOptions != nil {
		options.Timeout = config.HTTPOptions.Timeout
	}
	requestConfig.HTTPOptions = &options
	preparation := generationRequest{contents: contents, config: &requestConfig}
	httpRequest, err := preparation.prepare(ctx, g.config, model, streaming)
	if err != nil {
		return nil, nil, err
	}
	cancel := context.CancelFunc(func() {})
	if options.Timeout != nil && *options.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *options.Timeout)
	}
	// SDK request preparation has already canceled its temporary timeout.
	// The real request and its response body share this invocation's context.
	httpRequest = httpRequest.WithContext(ctx)
	dispatched = true
	response, err := g.config.HTTPClient.Do(httpRequest)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer response.Body.Close()
		defer cancel()
		var envelope struct {
			Error genai.APIError `json:"error"`
		}
		if decodeErr := jsonv2.UnmarshalRead(io.LimitReader(response.Body, maxGenerationEventBytes), &envelope); decodeErr != nil {
			return nil, nil, newResponseError(decodeErr, response.StatusCode, response.Header)
		}
		return nil, nil, newResponseError(envelope.Error, response.StatusCode, response.Header)
	}
	return response, cancel, nil
}

func (g *GenerationClient) requestHeaders(options *genai.HTTPOptions) (http.Header, error) {
	headers := make(http.Header, len(g.config.HTTPOptions.Headers))
	for name, values := range g.config.HTTPOptions.Headers {
		headers[http.CanonicalHeaderKey(name)] = append([]string(nil), values...)
	}
	if options == nil {
		return headers, nil
	}
	seen := make(map[string]struct{}, len(options.Headers))
	for name, values := range options.Headers {
		key := http.CanonicalHeaderKey(name)
		switch key {
		case headerAuthorization, headerProxyAuthorization, headerGoogleAPIKey, headerAPIKey, headerXAPIKey, headerCookie:
			return nil, fmt.Errorf("google: header %q is owned by the credential binding", key)
		}
		if _, duplicate := seen[key]; duplicate {
			return nil, fmt.Errorf("google: duplicate request header %q", key)
		}
		seen[key] = struct{}{}
		headers[key] = append([]string(nil), values...)
	}
	return headers, nil
}

func (g *GenerationClient) decode(raw []byte, headers http.Header) (*genai.GenerateContentResponse, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, fmt.Errorf("%w: frame must be an object", ErrInvalidGenerationResponse)
	}
	var wire struct {
		generationResponse
		Error *genai.APIError `json:"error"`
	}
	unmarshalers := []*jsonv2.Unmarshalers{
		jsonv2.UnmarshalFromFunc(unmarshalGenerationNumber),
		jsonv2.UnmarshalFromFunc(unmarshalGenerationPart),
	}
	if g.config.Backend == genai.BackendGeminiAPI {
		unmarshalers = append(unmarshalers, jsonv2.UnmarshalFromFunc(func(decoder *jsontext.Decoder, target *genai.CitationMetadata) error {
			var native struct {
				Sources []*genai.Citation `json:"citationSources"`
			}
			if err := jsonv2.UnmarshalDecode(decoder, &native); err != nil {
				return err
			}
			target.Citations = native.Sources
			return nil
		}))
	}
	if err := jsonv2.Unmarshal(raw, &wire, jsonv2.WithUnmarshalers(jsonv2.JoinUnmarshalers(unmarshalers...))); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidGenerationResponse, err)
	}
	if wire.Error != nil {
		return nil, newResponseError(*wire.Error, wire.Error.Code, headers)
	}
	if err := validateGenerationObjects(reflect.ValueOf(wire.generationResponse)); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidGenerationResponse, err)
	}
	wire.SDKHTTPResponse = &genai.HTTPResponse{Headers: headers.Clone(), Body: string(raw)}
	response := genai.GenerateContentResponse(wire.generationResponse)
	return &response, nil
}

// Defining the SDK shape without its custom v1 UnmarshalJSON keeps nested
// decoding, including Gemini's citationSources projection, in this boundary.
type generationResponse genai.GenerateContentResponse

func validateGenerationObjects(value reflect.Value) error {
	if value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return nil
		}
		return validateGenerationObjects(value.Elem())
	}
	switch value.Kind() {
	case reflect.Struct:
		for index := range value.NumField() {
			if !value.Field(index).CanInterface() {
				continue
			}
			if err := validateGenerationObjects(value.Field(index)); err != nil {
				return fmt.Errorf("%s: %w", value.Type().Field(index).Name, err)
			}
		}
	case reflect.Slice, reflect.Array:
		for index := range value.Len() {
			element := value.Index(index)
			if element.Kind() == reflect.Pointer && element.Type().Elem().Kind() == reflect.Struct && element.IsNil() {
				return fmt.Errorf("google: null generation object at index %d", index)
			}
			if err := validateGenerationObjects(element); err != nil {
				return err
			}
		}
	}
	return nil
}
