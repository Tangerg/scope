package protocol

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"google.golang.org/genai"
)

const (
	generationConfigField         = "generationConfig"
	generationContentsField       = "contents"
	generationSystemField         = "systemInstruction"
	generationPartsField          = "parts"
	generationToolsField          = "tools"
	generationDeclarationsField   = "functionDeclarations"
	generationResponseSchemaField = "responseJsonSchema"
	generationParametersField     = "parametersJsonSchema"
	generationFunctionCallField   = "functionCall"
	generationFunctionResultField = "functionResponse"
	generationServerCallField     = "toolCall"
	generationServerResultField   = "toolResponse"
	generationArgumentsField      = "args"
	generationResultField         = "response"
	generationPartMetadataField   = "partMetadata"
)

var errGenerationRequestPrepared = errors.New("google: generation request prepared")

// The public SDK owns backend admission and the complete request projection,
// but offers no request preparation API. This transport captures its request
// without I/O; a private sentinel prevents entering its unchecked response
// converters. The real request remains owned by GenerationClient.
type generationRequest struct {
	contents      []*genai.Content
	config        *genai.GenerateContentConfig
	prepared      *http.Request
	projectionErr error
}

func (g *generationRequest) prepare(ctx context.Context, clientConfig genai.ClientConfig, model string, streaming bool) (*http.Request, error) {
	if model == "" || strings.Contains(model, "..") || strings.ContainsAny(model, "?#&\r\n") {
		return nil, errors.New("google: invalid generation model")
	}
	if err := validateGenerationObjects(reflect.ValueOf(g.contents)); err != nil {
		return nil, err
	}
	if err := validateGenerationObjects(reflect.ValueOf(g.config)); err != nil {
		return nil, err
	}
	if _, err := jsonv2.Marshal(g.contents); err != nil {
		return nil, err
	}
	if _, err := jsonv2.Marshal(g.config, jsonv2.WithMarshalers(jsonv2.MarshalFunc(marshalGenerationDuration))); err != nil {
		return nil, err
	}
	config := *g.config
	config.AutomaticContinuation = new(false)
	if config.SystemInstruction != nil {
		config.SystemInstruction = new(*config.SystemInstruction)
	}
	options := genai.HTTPOptions{}
	if config.HTTPOptions != nil {
		options = *config.HTTPOptions
		options.Headers = options.Headers.Clone()
	}
	options.ExtrasRequestProvider = g.preserveJSON
	config.HTTPOptions = &options
	clientConfig.HTTPOptions.Headers = clientConfig.HTTPOptions.Headers.Clone()
	clientConfig.HTTPOptions.RetryOptions = nil
	// Cookies, redirects and authentication belong to the real client. Request
	// preparation must not invoke user transport or cookie-jar callbacks.
	clientConfig.HTTPClient = &http.Client{Transport: g, Timeout: clientConfig.HTTPClient.Timeout}
	if clientConfig.Backend == genai.BackendGeminiAPI {
		clientConfig.Project, clientConfig.Location = "", ""
	}
	client, err := genai.NewClient(ctx, &clientConfig)
	if err != nil {
		return nil, err
	}
	if streaming {
		for _, streamErr := range client.Models.GenerateContentStream(ctx, model, g.contents, &config) {
			err = streamErr
			break
		}
	} else {
		_, err = client.Models.GenerateContent(ctx, model, g.contents, &config)
	}
	if !errors.Is(err, errGenerationRequestPrepared) {
		if g.prepared != nil {
			g.prepared.Body.Close()
		}
		if err == nil {
			err = errors.New("google: SDK generation completed without preparing a request")
		}
		return nil, err
	}
	if g.prepared == nil {
		return nil, errors.New("google: SDK generation returned no prepared request")
	}
	return g.prepared, nil
}

func (g *generationRequest) RoundTrip(request *http.Request) (*http.Response, error) {
	defer request.Body.Close()
	if g.projectionErr != nil {
		return nil, g.projectionErr
	}
	if g.prepared != nil {
		return nil, errors.New("google: SDK attempted multiple generation requests during preparation")
	}
	if request.GetBody == nil {
		return nil, errors.New("google: SDK generation request body cannot be replayed")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	g.prepared = request.Clone(request.Context())
	g.prepared.Body = body
	return nil, errGenerationRequestPrepared
}

// The SDK decodes arbitrary JSON through float64 before its final encoder.
// Restore only opaque JSON payloads from their original owners after native
// projection; require each selected slot to exist so no rejected field returns.
func (g *generationRequest) preserveJSON(body map[string]any) map[string]any {
	g.projectionErr = g.restoreJSON(body)
	return body
}

func (g *generationRequest) restoreJSON(body map[string]any) error {
	if g.config.ResponseJsonSchema != nil {
		if err := restoreGenerationJSON(body[generationConfigField], generationResponseSchemaField, g.config.ResponseJsonSchema); err != nil {
			return err
		}
	}
	if err := restoreGenerationContentsJSON(body[generationContentsField], g.contents); err != nil {
		return err
	}
	if g.config.SystemInstruction != nil {
		if err := restoreGenerationContentJSON(body[generationSystemField], g.config.SystemInstruction); err != nil {
			return err
		}
	}
	if len(g.config.Tools) == 0 {
		return nil
	}
	tools, err := generationJSONArray(body[generationToolsField], len(g.config.Tools))
	if err != nil {
		return err
	}
	for index, tool := range g.config.Tools {
		if len(tool.FunctionDeclarations) == 0 {
			continue
		}
		target, ok := tools[index].(map[string]any)
		if !ok {
			return errors.New("google: SDK generation tool is not an object")
		}
		declarations, err := generationJSONArray(target[generationDeclarationsField], len(tool.FunctionDeclarations))
		if err != nil {
			return err
		}
		for index, declaration := range tool.FunctionDeclarations {
			if declaration.ParametersJsonSchema != nil {
				if err := restoreGenerationJSON(declarations[index], generationParametersField, declaration.ParametersJsonSchema); err != nil {
					return err
				}
			}
			if declaration.ResponseJsonSchema != nil {
				if err := restoreGenerationJSON(declarations[index], generationResponseSchemaField, declaration.ResponseJsonSchema); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func restoreGenerationContentsJSON(value any, contents []*genai.Content) error {
	if len(contents) == 0 {
		return nil
	}
	targets, err := generationJSONArray(value, len(contents))
	if err != nil {
		return err
	}
	for index, content := range contents {
		if err := restoreGenerationContentJSON(targets[index], content); err != nil {
			return err
		}
	}
	return nil
}

func restoreGenerationContentJSON(value any, content *genai.Content) error {
	if len(content.Parts) == 0 {
		return nil
	}
	target, ok := value.(map[string]any)
	if !ok {
		return errors.New("google: SDK generation content is not an object")
	}
	parts, err := generationJSONArray(target[generationPartsField], len(content.Parts))
	if err != nil {
		return err
	}
	for index, part := range content.Parts {
		if err := restoreGenerationPartJSON(parts[index], part); err != nil {
			return err
		}
	}
	return nil
}

func restoreGenerationPartJSON(value any, part *genai.Part) error {
	target, ok := value.(map[string]any)
	if !ok {
		return errors.New("google: SDK generation part is not an object")
	}
	if part.FunctionCall != nil && len(part.FunctionCall.Args) != 0 {
		if err := restoreGenerationJSON(target[generationFunctionCallField], generationArgumentsField, part.FunctionCall.Args); err != nil {
			return err
		}
	}
	if part.FunctionResponse != nil && len(part.FunctionResponse.Response) != 0 {
		if err := restoreGenerationJSON(target[generationFunctionResultField], generationResultField, part.FunctionResponse.Response); err != nil {
			return err
		}
	}
	if part.ToolCall != nil && len(part.ToolCall.Args) != 0 {
		if err := restoreGenerationJSON(target[generationServerCallField], generationArgumentsField, part.ToolCall.Args); err != nil {
			return err
		}
	}
	if part.ToolResponse != nil && len(part.ToolResponse.Response) != 0 {
		if err := restoreGenerationJSON(target[generationServerResultField], generationResultField, part.ToolResponse.Response); err != nil {
			return err
		}
	}
	if len(part.PartMetadata) != 0 {
		return restoreGenerationJSON(target, generationPartMetadataField, part.PartMetadata)
	}
	return nil
}

func generationJSONArray(value any, length int) ([]any, error) {
	var array []any
	switch value := value.(type) {
	case []any:
		array = value
	case []map[string]any:
		array = make([]any, len(value))
		for index, object := range value {
			array[index] = object
		}
	}
	if array == nil || len(array) != length {
		return nil, errors.New("google: SDK generation changed opaque JSON positions")
	}
	return array, nil
}

func restoreGenerationJSON(target any, field string, value any) error {
	object, ok := target.(map[string]any)
	if !ok {
		return fmt.Errorf("google: SDK generation parent of %s is not an object", field)
	}
	if _, found := object[field]; !found {
		return fmt.Errorf("google: SDK generation omitted opaque JSON field %s", field)
	}
	raw, err := jsonv2.Marshal(value)
	if err != nil {
		return err
	}
	object[field] = json.RawMessage(raw)
	return nil
}
