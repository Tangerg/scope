package google

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"time"

	"github.com/Tangerg/scope/core/image"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

const (
	imageInteractionCompleted   = "completed"
	imageInteractionModelOutput = "model_output"
	imageContentMetadataKey     = Provider + "/image_content"
)

type imageInteractionResponse struct {
	ID      string            `json:"id"`
	Model   string            `json:"model"`
	Status  string            `json:"status"`
	Object  string            `json:"object"`
	Created string            `json:"created"`
	Updated string            `json:"updated"`
	Steps   []json.RawMessage `json:"steps"`
	Usage   json.RawMessage   `json:"usage"`
	Raw     json.RawMessage   `json:"-"`
}

type imageInteractionStep struct {
	Type    string                     `json:"type"`
	Content []json.RawMessage          `json:"content"`
	Error   *imageInteractionStepError `json:"error,omitzero"`
}

type imageInteractionStepError struct {
	Code    int               `json:"code"`
	Message string            `json:"message"`
	Details []json.RawMessage `json:"details,omitempty"`
}

type imageInteractionOutput struct {
	Type       string `json:"type"`
	Data       []byte `json:"data,omitempty"`
	URI        string `json:"uri,omitempty"`
	MIMEType   string `json:"mime_type,omitempty"`
	Resolution string `json:"resolution,omitempty"`
}

func (i imageInteractionOutput) media() (*media.Media, error) {
	if i.MIMEType == "" {
		return nil, errors.New("image output has no MIME type")
	}
	switch {
	case len(i.Data) > 0 && i.URI == "":
		return media.NewBytes(i.MIMEType, i.Data)
	case len(i.Data) == 0 && i.URI != "":
		return media.NewURI(i.MIMEType, i.URI)
	case len(i.Data) == 0:
		return nil, errors.New("image output has neither inline data nor URI")
	default:
		return nil, errors.New("image output has both inline data and URI")
	}
}

func (i *imageInteractionResponse) response() (*image.Response, error) {
	if i == nil {
		return nil, errors.New("google: image: nil Interactions response")
	}
	if i.Status != imageInteractionCompleted {
		return nil, fmt.Errorf("google: image: Interactions status %q is not completed", i.Status)
	}
	var outputs []*image.Output
	for stepIndex, rawStep := range i.Steps {
		var step imageInteractionStep
		if err := jsonv2.Unmarshal(rawStep, &step); err != nil {
			return nil, fmt.Errorf("google: image: decode steps[%d]: %w", stepIndex, err)
		}
		if step.Error != nil {
			return nil, fmt.Errorf("google: image: steps[%d] failed with code %d: %s", stepIndex, step.Error.Code, step.Error.Message)
		}
		if step.Type != imageInteractionModelOutput {
			continue
		}
		for contentIndex, rawContent := range step.Content {
			var interactionOutput imageInteractionOutput
			if err := jsonv2.Unmarshal(rawContent, &interactionOutput); err != nil {
				return nil, fmt.Errorf("google: image: decode steps[%d].content[%d]: %w", stepIndex, contentIndex, err)
			}
			if interactionOutput.Type != imageInteractionType {
				continue
			}
			value, err := interactionOutput.media()
			if err != nil {
				return nil, fmt.Errorf("google: image: steps[%d].content[%d]: %w", stepIndex, contentIndex, err)
			}
			var outputMetadata metadata.Map
			if setErr := outputMetadata.Set(imageContentMetadataKey, rawContent); setErr != nil {
				return nil, setErr
			}
			output, err := image.NewOutput(value, outputMetadata)
			if err != nil {
				return nil, err
			}
			outputs = append(outputs, output)
		}
	}
	if len(outputs) == 0 {
		return nil, fmt.Errorf("google: image: Interactions response status %q has no model-output images", i.Status)
	}

	meta := &image.ResponseMetadata{}
	if i.Created != "" {
		created, err := time.Parse(time.RFC3339, i.Created)
		if err != nil {
			return nil, fmt.Errorf("google: image: invalid provider creation time %q: %w", i.Created, err)
		}
		meta.CreatedAt = created.UTC()
	}
	if err := meta.Extra.Set(ImageResponseExtensionKey, i.Raw); err != nil {
		return nil, err
	}
	return image.NewResponse(outputs, meta)
}
