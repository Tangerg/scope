package protocol

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

const (
	embeddingInstancesField     = "instances"
	embeddingContentField       = "content"
	embeddingParametersField    = "parameters"
	embeddingContentConfigField = "embedContentConfig"
	embeddingAutoTruncateField  = "autoTruncate"
)

// The SDK omits false and its body hook cannot return an error. One call owns
// both the optional value and any failed projection; its transport rejects that
// failure before dispatch instead of sending a request with different semantics.
type embeddingRequest struct {
	model         string
	contents      []*genai.Content
	config        *genai.EmbedContentConfig
	autoTruncate  *bool
	projectionErr error
	dispatched    bool
}

func (e *embeddingRequest) configure(backend genai.Backend) error {
	if e.autoTruncate == nil {
		return nil
	}
	if backend != genai.BackendVertexAI {
		return fmt.Errorf("google: embedding: %w: autoTruncate requires Vertex AI", errors.ErrUnsupported)
	}
	e.config.HTTPOptions = &genai.HTTPOptions{ExtrasRequestProvider: e.projectTruncation}
	return nil
}

func (e *embeddingRequest) projectTruncation(body map[string]any) map[string]any {
	if e.projectionErr != nil {
		return body
	}
	_, predict := body[embeddingInstancesField]
	_, embedContent := body[embeddingContentField]
	if predict == embedContent {
		e.projectionErr = errors.New("google: embedding: SDK request requires exactly one native input envelope")
		return body
	}
	field := embeddingContentConfigField
	if predict {
		field = embeddingParametersField
	}
	var config map[string]any
	if value, exists := body[field]; exists {
		var ok bool
		config, ok = value.(map[string]any)
		if !ok || config == nil {
			e.projectionErr = fmt.Errorf("google: embedding: SDK request %s must be an object", field)
			return body
		}
	} else {
		config = make(map[string]any)
		body[field] = config
	}
	config[embeddingAutoTruncateField] = *e.autoTruncate
	return body
}
