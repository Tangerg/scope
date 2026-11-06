package protocol

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"iter"
	"net/http"
	"net/url"
	"reflect"
	"slices"
	"strings"

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

// GenerationClient owns GenerateContent's native request and response boundary.
// The SDK supplies authentication, DTOs, and generation-config projection; its
// unchecked response converters are never entered.
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
		return nil, err
	}
	if len(raw) > maxGenerationEventBytes {
		return nil, errors.New("google: generation response exceeds byte limit")
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
				yield(nil, eventErr)
				return
			}
			chunk, decodeErr := g.decode(event.Data, response.Header)
			if decodeErr != nil {
				yield(nil, decodeErr)
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
		if err != nil && !dispatched {
			err = &invocation.Failure{Kind: invocation.FailureNotExecuted, Cause: err}
		}
	}()
	if config == nil {
		config = &genai.GenerateContentConfig{}
	}
	if config.AutomaticContinuation != nil && *config.AutomaticContinuation {
		return nil, nil, errors.New("google: automatic continuation is unsupported; each call owns one native generation")
	}
	if config.HTTPOptions != nil {
		raw, marshalErr := jsonv2.Marshal(config.HTTPOptions, jsonv2.OmitZeroStructFields(true))
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		var fields map[string]json.RawMessage
		if decodeErr := jsonv2.Unmarshal(raw, &fields); decodeErr != nil {
			return nil, nil, decodeErr
		}
		for field := range fields {
			if field != "headers" && field != "timeout" {
				return nil, nil, fmt.Errorf("google: unsupported generation HTTP option %q", field)
			}
		}
	}
	request := generationRequest{
		Contents: contents, SystemInstruction: config.SystemInstruction,
		Tools: config.Tools, ToolConfig: config.ToolConfig, SafetySettings: config.SafetySettings,
		CachedContent: config.CachedContent, Labels: config.Labels, ModelArmorConfig: config.ModelArmorConfig,
		ServiceTier: config.ServiceTier, ContinuationToken: config.ContinuationToken,
	}
	if g.config.Backend == genai.BackendGeminiAPI && config.ModelArmorConfig != nil {
		return nil, nil, errors.New("google: modelArmorConfig requires Vertex AI")
	}
	if request.CachedContent != "" && !strings.Contains(request.CachedContent, "/") {
		request.CachedContent = "cachedContents/" + request.CachedContent
	}
	if g.config.Backend == genai.BackendVertexAI && strings.HasPrefix(request.CachedContent, "cachedContents/") {
		request.CachedContent = fmt.Sprintf("projects/%s/locations/%s/%s", g.config.Project, g.config.Location, request.CachedContent)
	}
	// These SDK configuration fields belong to the native request envelope,
	// while ToGenerationConfig owns the remaining generationConfig projection.
	generation := *config
	generation.HTTPOptions, generation.SystemInstruction = nil, nil
	generation.Tools, generation.ToolConfig, generation.SafetySettings = nil, nil, nil
	generation.CachedContent, generation.Labels, generation.ModelArmorConfig = "", nil, nil
	generation.ServiceTier, generation.ContinuationToken, generation.AutomaticContinuation = "", nil, nil
	// The SDK's configuration conversion decodes open JSON through float64.
	// Keep the Core-owned schema outside that conversion.
	generation.ResponseJsonSchema = nil
	request.GenerationConfig, err = generation.ToGenerationConfig(g.config.Backend)
	if err != nil {
		return nil, nil, err
	}
	request.GenerationConfig.ResponseJsonSchema = config.ResponseJsonSchema
	raw, err := jsonv2.Marshal(nativeRequestValue(reflect.ValueOf(request)))
	if err != nil {
		return nil, nil, err
	}
	endpoint, err := g.endpoint(model, streaming)
	if err != nil {
		return nil, nil, err
	}
	cancel := context.CancelFunc(func() {})
	if config.HTTPOptions != nil && config.HTTPOptions.Timeout != nil && *config.HTTPOptions.Timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, *config.HTTPOptions.Timeout)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(raw))
	if err != nil {
		cancel()
		return nil, nil, err
	}
	httpRequest.Header, err = g.requestHeaders(config.HTTPOptions)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	if g.config.APIKey != "" {
		httpRequest.Header.Set(headerGoogleAPIKey, g.config.APIKey)
	}
	dispatched = true
	response, err := g.config.HTTPClient.Do(httpRequest)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
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

func (g *GenerationClient) endpoint(model string, streaming bool) (string, error) {
	if model == "" || strings.Contains(model, "..") || strings.ContainsAny(model, "?#&\r\n") {
		return "", errors.New("google: invalid generation model")
	}
	if g.config.Backend == genai.BackendVertexAI {
		if !strings.HasPrefix(model, "projects/") && !strings.HasPrefix(model, "models/") && !strings.HasPrefix(model, "publishers/") {
			if publisher, name, found := strings.Cut(model, "/"); found {
				model = fmt.Sprintf("publishers/%s/models/%s", publisher, name)
			} else {
				model = "publishers/google/models/" + model
			}
		}
		if !strings.HasPrefix(model, "projects/") {
			model = fmt.Sprintf("projects/%s/locations/%s/%s", g.config.Project, g.config.Location, model)
		}
	} else if !strings.HasPrefix(model, "models/") && !strings.HasPrefix(model, "tunedModels/") {
		model = "models/" + model
	}
	action := ":generateContent"
	if streaming {
		action = ":streamGenerateContent"
	}
	endpoint, err := url.JoinPath(g.config.HTTPOptions.BaseURL, g.config.HTTPOptions.APIVersion, model+action)
	if streaming {
		endpoint += "?alt=sse"
	}
	return endpoint, err
}

func (g *GenerationClient) decode(raw []byte, headers http.Header) (*genai.GenerateContentResponse, error) {
	if trimmed := bytes.TrimSpace(raw); len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errors.New("google: generation frame must be an object")
	}
	var wire struct {
		generationResponse
		Error *genai.APIError `json:"error"`
	}
	unmarshalers := []*jsonv2.Unmarshalers{jsonv2.UnmarshalFromFunc(func(decoder *jsontext.Decoder, value *any) error {
		if decoder.PeekKind() != jsontext.KindNumber {
			return errors.ErrUnsupported
		}
		raw, err := decoder.ReadValue()
		if err != nil {
			return err
		}
		*value = json.Number(raw)
		return nil
	})}
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
		return nil, fmt.Errorf("google: invalid generation frame: %w", err)
	}
	if wire.Error != nil {
		return nil, newResponseError(*wire.Error, wire.Error.Code, headers)
	}
	if err := validateGenerationObjects(reflect.ValueOf(wire.generationResponse)); err != nil {
		return nil, err
	}
	wire.SDKHTTPResponse = &genai.HTTPResponse{Headers: headers.Clone(), Body: string(raw)}
	response := genai.GenerateContentResponse(wire.generationResponse)
	return &response, nil
}

// Defining the SDK shape without its custom v1 UnmarshalJSON keeps nested
// decoding, including Gemini's citationSources projection, in this boundary.
type generationResponse genai.GenerateContentResponse

type generationRequest struct {
	Contents          []*genai.Content        `json:"contents"`
	SystemInstruction *genai.Content          `json:"systemInstruction,omitempty"`
	Tools             []*genai.Tool           `json:"tools,omitempty"`
	ToolConfig        *genai.ToolConfig       `json:"toolConfig,omitempty"`
	SafetySettings    []*genai.SafetySetting  `json:"safetySettings,omitempty"`
	CachedContent     string                  `json:"cachedContent,omitempty"`
	Labels            map[string]string       `json:"labels,omitempty"`
	ModelArmorConfig  *genai.ModelArmorConfig `json:"modelArmorConfig,omitempty"`
	ServiceTier       genai.ServiceTier       `json:"serviceTier,omitempty"`
	ContinuationToken []byte                  `json:"continuationToken,omitempty"`
	GenerationConfig  *genai.GenerationConfig `json:"generationConfig"`
}

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

// SDK tags describe Go-value absence. Project them before v2 encoding so a
// selected empty native object (such as googleSearch:{}) is not omitted. The
// SDK declarations remain the only source of field names and request shape.
func nativeRequestValue(value reflect.Value) any {
	if !value.IsValid() {
		return nil
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil
		}
		if value.Type().Implements(reflect.TypeFor[json.Marshaler]()) {
			return value.Interface()
		}
		return nativeRequestValue(value.Elem())
	}
	if value.Type().Implements(reflect.TypeFor[json.Marshaler]()) {
		return value.Interface()
	}
	switch value.Kind() {
	case reflect.Struct:
		object := make(map[string]any)
		for index := range value.NumField() {
			field := value.Type().Field(index)
			if !field.IsExported() {
				continue
			}
			tag := strings.Split(field.Tag.Get("json"), ",")
			name := tag[0]
			if name == "-" {
				continue
			}
			if name == "" {
				name = field.Name
			}
			item := value.Field(index)
			if slices.Contains(tag[1:], "omitzero") && item.IsZero() {
				continue
			}
			if slices.Contains(tag[1:], "omitempty") {
				switch item.Kind() {
				case reflect.Array, reflect.Map, reflect.Slice, reflect.String:
					if item.Len() == 0 {
						continue
					}
				case reflect.Pointer, reflect.Interface, reflect.Bool, reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64, reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Float32, reflect.Float64:
					if item.IsZero() {
						continue
					}
				}
			}
			object[name] = nativeRequestValue(item)
		}
		return object
	case reflect.Slice, reflect.Array:
		if value.Type().Elem().Kind() == reflect.Uint8 {
			return value.Interface()
		}
		items := make([]any, value.Len())
		for index := range value.Len() {
			items[index] = nativeRequestValue(value.Index(index))
		}
		return items
	case reflect.Map:
		if value.IsNil() {
			return nil
		}
		if value.Type().Key().Kind() != reflect.String {
			return value.Interface()
		}
		object := make(map[string]any, value.Len())
		for iterator := value.MapRange(); iterator.Next(); {
			object[iterator.Key().String()] = nativeRequestValue(iterator.Value())
		}
		return object
	default:
		return value.Interface()
	}
}
