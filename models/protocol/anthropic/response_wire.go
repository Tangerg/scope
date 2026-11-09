package anthropic

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
)

// The SDK coerces malformed scalar values. Admission reads the original event,
// so those conversions cannot manufacture text, identities, or replay state.
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

type responseCitationWire struct {
	Type              responseWireString  `json:"type"`
	CitedText         responseWireString  `json:"cited_text"`
	DocumentTitle     *responseWireString `json:"document_title"`
	FileID            *responseWireString `json:"file_id"`
	URL               responseWireString  `json:"url"`
	Title             *responseWireString `json:"title"`
	Source            responseWireString  `json:"source"`
	EncryptedIndex    responseWireString  `json:"encrypted_index"`
	DocumentIndex     int64               `json:"document_index"`
	SearchResultIndex int64               `json:"search_result_index"`
	StartCharIndex    int64               `json:"start_char_index"`
	EndCharIndex      int64               `json:"end_char_index"`
	StartPageNumber   int64               `json:"start_page_number"`
	EndPageNumber     int64               `json:"end_page_number"`
	StartBlockIndex   int64               `json:"start_block_index"`
	EndBlockIndex     int64               `json:"end_block_index"`
}

type responseBlockWire struct {
	Type      responseWireString     `json:"type"`
	Text      responseWireString     `json:"text"`
	ID        responseWireString     `json:"id"`
	Name      responseWireString     `json:"name"`
	Thinking  responseWireString     `json:"thinking"`
	Signature responseWireString     `json:"signature"`
	Data      responseWireString     `json:"data"`
	Citations []responseCitationWire `json:"citations"`
}

type responseEventWire struct {
	Type         responseWireString `json:"type"`
	Index        int64              `json:"index"`
	ContentBlock *responseBlockWire `json:"content_block"`
	Message      *struct {
		ID           responseWireString  `json:"id"`
		Type         responseWireString  `json:"type"`
		Role         responseWireString  `json:"role"`
		Model        responseWireString  `json:"model"`
		StopReason   *responseWireString `json:"stop_reason"`
		StopSequence *responseWireString `json:"stop_sequence"`
		Content      []responseBlockWire `json:"content"`
	} `json:"message"`
	Delta *struct {
		Type         responseWireString    `json:"type"`
		Text         responseWireString    `json:"text"`
		Thinking     responseWireString    `json:"thinking"`
		Signature    responseWireString    `json:"signature"`
		PartialJSON  responseWireString    `json:"partial_json"`
		StopReason   *responseWireString   `json:"stop_reason"`
		StopSequence *responseWireString   `json:"stop_sequence"`
		Citation     *responseCitationWire `json:"citation"`
	} `json:"delta"`
}

func validateResponseWire[T any](raw string) error {
	if jsontext.Value(raw).Kind() != '{' {
		return errors.New("response must be a JSON object")
	}
	var event T
	return jsonv2.Unmarshal([]byte(raw), &event)
}

var _ json.Unmarshaler = (*responseWireString)(nil)
