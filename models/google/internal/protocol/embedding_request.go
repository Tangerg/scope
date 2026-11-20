package protocol

import (
	"errors"
	"fmt"

	"google.golang.org/genai"
)

const (
	embeddingInstancesField     = "instances"
	embeddingContentField       = "content"
	embeddingRequestsField      = "requests"
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
	configs, err := truncationTargets(body)
	if err != nil {
		e.projectionErr = err
		return body
	}
	for _, config := range configs {
		config[embeddingAutoTruncateField] = *e.autoTruncate
	}
	return body
}

// truncationTargets returns the configuration objects of the request's one
// native input envelope: the predict parameters, the embedContent config, or
// the config of every batchEmbedContents request.
func truncationTargets(body map[string]any) ([]map[string]any, error) {
	_, predict := body[embeddingInstancesField]
	_, single := body[embeddingContentField]
	requests, batch := body[embeddingRequestsField]
	switch {
	case predict && !single && !batch:
		config, err := objectMember(body, embeddingParametersField)
		return []map[string]any{config}, err
	case single && !predict && !batch:
		config, err := objectMember(body, embeddingContentConfigField)
		return []map[string]any{config}, err
	case batch && !predict && !single:
		entries, ok := requests.([]map[string]any)
		if !ok || len(entries) == 0 {
			return nil, fmt.Errorf("google: embedding: SDK request %s must be a nonempty list", embeddingRequestsField)
		}
		configs := make([]map[string]any, 0, len(entries))
		for _, entry := range entries {
			config, err := objectMember(entry, embeddingContentConfigField)
			if err != nil {
				return nil, err
			}
			configs = append(configs, config)
		}
		return configs, nil
	default:
		return nil, errors.New("google: embedding: SDK request requires exactly one native input envelope")
	}
}

// objectMember returns parent's object member field, adding an empty one when
// the SDK omitted it.
func objectMember(parent map[string]any, field string) (map[string]any, error) {
	value, exists := parent[field]
	if !exists {
		config := make(map[string]any)
		parent[field] = config
		return config, nil
	}
	config, ok := value.(map[string]any)
	if !ok || config == nil {
		return nil, fmt.Errorf("google: embedding: SDK request %s must be an object", field)
	}
	return config, nil
}
