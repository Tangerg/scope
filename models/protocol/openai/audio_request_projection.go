package openai

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared/constant"

	"github.com/Tangerg/scope/core/metadata"
)

type audioRequestParams interface {
	openai.AudioTranscriptionNewParams | openai.AudioTranslationNewParams
}

type audioMultipartObject interface {
	audioRequestParams | openai.AudioTranscriptionNewParamsChunkingStrategyVadConfig
}

func decodeAudioRequest[T audioRequestParams](extensions metadata.Extensions, key string, coreFields ...string) (*T, error) {
	raw, found, err := extensions.Decode[json.RawMessage](key)
	if err != nil {
		return nil, fmt.Errorf("openai: extension %q: %w", key, err)
	}
	params := new(T)
	if !found {
		return params, nil
	}
	fields, err := decodeRequestFields(extensions, key, append(coreFields, "stream")...)
	if err != nil {
		return nil, err
	}
	if fields == nil {
		return nil, fmt.Errorf("openai: extension %q must be an object", key)
	}
	if err := validateMultipartValue(map[string]any(fields)); err != nil {
		return nil, fmt.Errorf("openai: extension %q: %w", key, err)
	}
	if format, exists := fields["response_format"]; exists {
		switch format {
		case string(openai.AudioResponseFormatJSON), string(openai.AudioResponseFormatVerboseJSON), string(openai.AudioResponseFormatDiarizedJSON):
		default:
			return nil, fmt.Errorf("openai: extension %q response_format %v requires an unsupported response decoder", key, format)
		}
	}
	// The SDK's multipart ExtraFields writer only accepts strings and stops at
	// the first other value. Its typed parameters preserve zeroes and arrays.
	if err := decodeAudioMultipartObject(raw, params); err != nil {
		return nil, fmt.Errorf("openai: extension %q: %w", key, err)
	}
	return params, nil
}

func decodeAudioMultipartObject[T audioMultipartObject](raw []byte, target *T) error {
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return errors.New("multipart fields must be an object")
	}
	// The SDK embeds private metadata that JSON v2 cannot decode. Reading its
	// exported field definitions avoids a second copy of the native schema.
	value := reflect.ValueOf(target).Elem()
	typeInfo := value.Type()
	for index := range typeInfo.NumField() {
		field := typeInfo.Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if !field.IsExported() || name == "" || name == "-" {
			continue
		}
		encoded, found := fields[name]
		if !found {
			continue
		}
		if err := jsonv2.Unmarshal(encoded, value.Field(index).Addr().Interface(), jsonv2.RejectUnknownMembers(true),
			jsonv2.WithUnmarshalers(jsonv2.UnmarshalFunc(decodeAudioChunkingStrategy))); err != nil {
			return fmt.Errorf("field %q: %w", name, err)
		}
		delete(fields, name)
	}
	if len(fields) != 0 {
		return fmt.Errorf("unsupported multipart fields %q", slices.Sorted(maps.Keys(fields)))
	}
	return nil
}

func validateMultipartValue(value any) error {
	switch value := value.(type) {
	case nil:
		return errors.New("null has no supported multipart representation")
	case map[string]any:
		for name, member := range value {
			if err := validateMultipartValue(member); err != nil {
				return fmt.Errorf("field %q: %w", name, err)
			}
		}
	case []any:
		if len(value) == 0 {
			return errors.New("empty arrays have no supported multipart representation")
		}
		for index, element := range value {
			if err := validateMultipartValue(element); err != nil {
				return fmt.Errorf("element %d: %w", index, err)
			}
		}
	}
	return nil
}

func decodeAudioChunkingStrategy(raw []byte, strategy *openai.AudioTranscriptionNewParamsChunkingStrategyUnion) error {
	var mode string
	if err := jsonv2.Unmarshal(raw, &mode); err == nil {
		automatic := constant.ValueOf[constant.Auto]()
		if mode != string(automatic) {
			return fmt.Errorf("unsupported chunking_strategy %q", mode)
		}
		strategy.OfAuto = automatic
		return nil
	}
	// The SDK union decoder accepts unknown object members that its multipart
	// encoder cannot preserve, so its VAD branch needs the same strict projection.
	var vad openai.AudioTranscriptionNewParamsChunkingStrategyVadConfig
	if err := decodeAudioMultipartObject(raw, &vad); err != nil {
		return err
	}
	if vad.Type != string(constant.ValueOf[constant.ServerVad]()) {
		return fmt.Errorf("unsupported chunking_strategy type %q", vad.Type)
	}
	strategy.OfAudioTranscriptionNewsChunkingStrategyVadConfig = &vad
	return nil
}
