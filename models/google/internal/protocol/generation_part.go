package protocol

import (
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"

	"google.golang.org/genai"
)

func validateGenerationPart(part *genai.Part) error {
	if err := validateGenerationPartData(part, false); err != nil {
		return err
	}
	switch {
	case part.ExecutableCode != nil:
		return errors.New("executableCode output and replay cannot be represented")
	case part.CodeExecutionResult != nil:
		return errors.New("codeExecutionResult output and replay cannot be represented")
	default:
		return nil
	}
}

func validateGenerationPartData(part *genai.Part, textPresent bool) error {
	if part == nil {
		return errors.New("nil generation part")
	}
	selected := false
	for _, present := range []bool{
		textPresent || part.Text != "", part.InlineData != nil, part.FileData != nil,
		part.FunctionCall != nil, part.FunctionResponse != nil,
		part.ToolCall != nil, part.ToolResponse != nil,
		part.ExecutableCode != nil, part.CodeExecutionResult != nil,
	} {
		if present && selected {
			return errors.New("generation part contains multiple data payloads")
		}
		selected = selected || present
	}
	return nil
}

func unmarshalGenerationPart(decoder *jsontext.Decoder, part *genai.Part) error {
	raw, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	var decoded generationPart
	if err := jsonv2.Unmarshal(raw, &decoded, jsonv2.WithUnmarshalers(jsonv2.UnmarshalFromFunc(unmarshalGenerationNumber))); err != nil {
		return err
	}
	var presence struct {
		Text *string `json:"text"`
	}
	if err := jsonv2.Unmarshal(raw, &presence); err != nil {
		return err
	}
	value := genai.Part(decoded)
	if err := validateGenerationPartData(&value, presence.Text != nil); err != nil {
		return err
	}
	*part = value
	return nil
}

func unmarshalGenerationNumber(decoder *jsontext.Decoder, value *any) error {
	if decoder.PeekKind() != jsontext.KindNumber {
		return errors.ErrUnsupported
	}
	raw, err := decoder.ReadValue()
	if err != nil {
		return err
	}
	*value = json.Number(raw)
	return nil
}

type generationPart genai.Part
