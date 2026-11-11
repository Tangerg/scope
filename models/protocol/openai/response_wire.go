package openai

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"github.com/openai/openai-go/v3/packages/respjson"
	"github.com/openai/openai-go/v3/responses"

	corechat "github.com/Tangerg/scope/core/chat"
)

// SDK response strings may contain coerced numbers, booleans, or objects.
// These transient wire shapes admit their original types before projection;
// unknown provider fields remain opaque, and lifecycle rules stay with streams.
type responseWireString string

func (r *responseWireString) UnmarshalJSON(data []byte) error {
	var value *string
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		return err
	}
	if value == nil {
		return errors.New("response field must be a string")
	}
	*r = responseWireString(*value)
	return nil
}

func validateResponseWire[T any](raw string) error {
	if jsontext.Value(raw).Kind() != jsontext.KindBeginObject {
		return errors.New("response must be a JSON object")
	}
	var value T
	return jsonv2.Unmarshal([]byte(raw), &value)
}

type chatChunkWire struct {
	ID      responseWireString `json:"id"`
	Model   responseWireString `json:"model"`
	Object  responseWireString `json:"object"`
	Created int64              `json:"created"`
	Choices []struct {
		Index        int64               `json:"index"`
		FinishReason *responseWireString `json:"finish_reason"`
		Delta        struct {
			Role      responseWireString  `json:"role"`
			Content   *responseWireString `json:"content"`
			Refusal   *responseWireString `json:"refusal"`
			ToolCalls []struct {
				Index    int64              `json:"index"`
				ID       responseWireString `json:"id"`
				Type     responseWireString `json:"type"`
				Function struct {
					Name      responseWireString `json:"name"`
					Arguments responseWireString `json:"arguments"`
				} `json:"function"`
			} `json:"tool_calls"`
			Audio *struct {
				ID         responseWireString `json:"id"`
				Data       responseWireString `json:"data"`
				Transcript responseWireString `json:"transcript"`
				ExpiresAt  int64              `json:"expires_at"`
			} `json:"audio"`
			Annotations []struct {
				Type        responseWireString   `json:"type"`
				URLCitation responseCitationWire `json:"url_citation"`
			} `json:"annotations"`
		} `json:"delta"`
	} `json:"choices"`
}

type responseCitationWire struct {
	Type        responseWireString `json:"type"`
	URL         responseWireString `json:"url"`
	Title       responseWireString `json:"title"`
	FileID      responseWireString `json:"file_id"`
	Filename    responseWireString `json:"filename"`
	ContainerID responseWireString `json:"container_id"`
	StartIndex  int64              `json:"start_index"`
	EndIndex    int64              `json:"end_index"`
	Index       int64              `json:"index"`
}

type responsesContentWire struct {
	Type        responseWireString     `json:"type"`
	Text        responseWireString     `json:"text"`
	Refusal     responseWireString     `json:"refusal"`
	Annotations []responseCitationWire `json:"annotations"`
}

type responsesItemWire struct{}

func (*responsesItemWire) UnmarshalJSON(data []byte) error {
	var identity struct {
		Type responseWireString `json:"type"`
	}
	if err := jsonv2.Unmarshal(data, &identity); err != nil {
		return err
	}
	switch string(identity.Type) {
	case responsesItemTypeMessage:
		var message struct {
			ID      responseWireString     `json:"id"`
			Status  responseWireString     `json:"status"`
			Role    responseWireString     `json:"role"`
			Phase   *responseWireString    `json:"phase"`
			Content []responsesContentWire `json:"content"`
		}
		return jsonv2.Unmarshal(data, &message)
	case responsesItemTypeFunctionCall:
		var call struct {
			ID        responseWireString `json:"id"`
			Status    responseWireString `json:"status"`
			CallID    responseWireString `json:"call_id"`
			Name      responseWireString `json:"name"`
			Arguments responseWireString `json:"arguments"`
		}
		return jsonv2.Unmarshal(data, &call)
	case responsesItemTypeReasoning:
		var reasoning struct {
			ID               responseWireString     `json:"id"`
			Status           responseWireString     `json:"status"`
			Content          []responsesContentWire `json:"content"`
			Summary          []responsesContentWire `json:"summary"`
			EncryptedContent *responseWireString    `json:"encrypted_content"`
		}
		return jsonv2.Unmarshal(data, &reasoning)
	default:
		return nil
	}
}

type responsesSnapshotEventWire struct {
	Response *struct {
		ID                responseWireString  `json:"id"`
		Model             responseWireString  `json:"model"`
		Status            responseWireString  `json:"status"`
		CreatedAt         float64             `json:"created_at"`
		Output            []responsesItemWire `json:"output"`
		IncompleteDetails *struct {
			Reason *responseWireString `json:"reason"`
		} `json:"incomplete_details"`
		Error *struct {
			Code    responseWireString `json:"code"`
			Message responseWireString `json:"message"`
		} `json:"error"`
	} `json:"response"`
}

type responsesItemEventWire struct {
	Item *responsesItemWire `json:"item"`
}

type responsesDeltaEventWire struct {
	ItemID responseWireString `json:"item_id"`
	Delta  responseWireString `json:"delta"`
}

type responsesArgumentsDoneEventWire struct {
	ItemID    responseWireString `json:"item_id"`
	Arguments responseWireString `json:"arguments"`
}

type responsesAnnotationEventWire struct {
	ItemID     responseWireString    `json:"item_id"`
	Annotation *responseCitationWire `json:"annotation"`
}

type responsesContentEventWire struct {
	ItemID  responseWireString    `json:"item_id"`
	Text    responseWireString    `json:"text"`
	Refusal responseWireString    `json:"refusal"`
	Part    *responsesContentWire `json:"part"`
}

type responsesErrorEventWire struct {
	Code    *responseWireString `json:"code"`
	Message responseWireString  `json:"message"`
}

func validateResponsesEvent[T any](event responses.ResponseStreamEventUnion) error {
	if err := validateResponseWire[T](event.RawJSON()); err != nil {
		return fmt.Errorf("%w: openai responses: event: %w", corechat.ErrInvalidResponse, err)
	}
	for _, position := range []struct {
		name  string
		field respjson.Field
	}{
		{"output_index", event.JSON.OutputIndex}, {"content_index", event.JSON.ContentIndex},
		{"summary_index", event.JSON.SummaryIndex}, {"annotation_index", event.JSON.AnnotationIndex},
	} {
		if position.field.Raw() != "" {
			if _, valid := responseIndex(position.field); !valid {
				return fmt.Errorf("%w: openai responses: %s must be a nonnegative integer", corechat.ErrInvalidResponse, position.name)
			}
		}
	}
	return nil
}

func transcriptionWireText(raw string) (string, error) {
	var response struct {
		Text *responseWireString `json:"text"`
	}
	if err := jsonv2.Unmarshal([]byte(raw), &response); err != nil {
		return "", fmt.Errorf("text response: %w", err)
	}
	if response.Text == nil {
		return "", errors.New("text response requires a string text")
	}
	return string(*response.Text), nil
}

var _ json.Unmarshaler = (*responseWireString)(nil)
