package protocol

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"google.golang.org/genai"
)

const serverToolPartsMetadataSuffix = "/trailing_server_tool_parts"

// Server tools execute at Google, so their payload has no Core tool-call owner.
// Runs precede the next portable part, or follow the message when terminal.
// This preserves their order without retaining a second copy of Core content.
type serverToolParts []*genai.Part

func (s serverToolParts) MarshalJSON() ([]byte, error) {
	if err := s.validate(); err != nil {
		return nil, err
	}
	parts := make([]json.RawMessage, len(s))
	for index, part := range s {
		// SDK omitempty follows v1 semantics: a selected empty server-tool
		// object still identifies the Part's data and must survive v2 storage.
		wire := struct {
			generationPart
			ToolCall     *genai.ToolCall     `json:"toolCall,omitzero"`
			ToolResponse *genai.ToolResponse `json:"toolResponse,omitzero"`
		}{generationPart(*part), part.ToolCall, part.ToolResponse}
		raw, err := jsonv2.Marshal(wire)
		if err != nil {
			return nil, err
		}
		parts[index] = raw
	}
	return jsonv2.Marshal(parts)
}

func (s serverToolParts) validate() error {
	for index, part := range s {
		if err := validateGenerationPart(part); err != nil {
			return fmt.Errorf("server tool parts[%d]: %w", index, err)
		}
		if part.ToolCall == nil && part.ToolResponse == nil {
			return errors.New("server tool replay must not contain Core-owned content")
		}
	}
	return nil
}

func (s *serverToolParts) UnmarshalJSON(data []byte) error {
	var decoded []*genai.Part
	if err := jsonv2.Unmarshal(data, &decoded, jsonv2.WithUnmarshalers(jsonv2.JoinUnmarshalers(
		jsonv2.UnmarshalFromFunc(unmarshalGenerationNumber),
		jsonv2.UnmarshalFromFunc(unmarshalGenerationPart),
	))); err != nil {
		return err
	}
	value := serverToolParts(decoded)
	if len(value) == 0 {
		return errors.New("server tool replay must contain at least one part")
	}
	if err := value.validate(); err != nil {
		return err
	}
	*s = value
	return nil
}
