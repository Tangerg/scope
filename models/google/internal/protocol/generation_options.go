package protocol

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
)

const (
	generationCodeExecutionField = "codeExecution"
	generationHTTPOptionsField   = "httpOptions"
	generationHeadersField       = "headers"
	generationTimeoutField       = "timeout"
)

func decodeSingleCandidateConfig(extensions metadata.Extensions, key string) (*genai.GenerateContentConfig, map[string]json.RawMessage, error) {
	raw, found, err := extensions.Decode[json.RawMessage](key)
	if err != nil {
		return nil, nil, fmt.Errorf("google: extension %q: %w", key, err)
	}
	config := &genai.GenerateContentConfig{}
	if !found {
		return config, nil, nil
	}
	var fields map[string]json.RawMessage
	if err := jsonv2.Unmarshal(raw, &fields); err != nil {
		return nil, nil, fmt.Errorf("google: extension %q: %w", key, err)
	}
	if fields == nil {
		return nil, nil, fmt.Errorf("google: extension %q must be an object", key)
	}
	if value, exists := fields[generationHTTPOptionsField]; exists {
		var options map[string]json.RawMessage
		if err := jsonv2.Unmarshal(value, &options); err != nil {
			return nil, nil, fmt.Errorf("google: extension %q HTTP options: %w", key, err)
		}
		for field := range options {
			if !isGenerationHTTPOption(field) {
				return nil, nil, fmt.Errorf("google: unsupported generation HTTP option %q", field)
			}
		}
	}
	if err := jsonv2.Unmarshal(raw, config, jsonv2.RejectUnknownMembers(true), jsonv2.WithUnmarshalers(jsonv2.JoinUnmarshalers(
		jsonv2.UnmarshalFunc(unmarshalGenerationDuration), jsonv2.UnmarshalFromFunc(unmarshalGenerationNumber),
	))); err != nil {
		return nil, nil, fmt.Errorf("google: extension %q: %w", key, err)
	}
	// The SDK omits a zero count; missing and null also select its single-candidate default.
	if count := config.CandidateCount; count < 0 || count > 1 {
		return nil, nil, fmt.Errorf("google: extension %q requests %d candidates for a single-output generation", key, count)
	}
	if rawTools, found := fields[generationToolsField]; found {
		var tools []map[string]json.RawMessage
		if err := jsonv2.Unmarshal(rawTools, &tools); err != nil {
			return nil, nil, fmt.Errorf("google: extension %q field %s: %w", key, generationToolsField, err)
		}
		for index, tool := range tools {
			if _, found := tool[generationCodeExecutionField]; found {
				return nil, nil, fmt.Errorf("google: extension %q field %s[%d].%s: %w: code execution output and replay cannot be represented", key, generationToolsField, index, generationCodeExecutionField, errors.ErrUnsupported)
			}
		}
	}
	return config, fields, nil
}

// The SDK's Go JSON representation uses integer nanoseconds for time.Duration;
// v2 requires an explicit representation instead of inheriting v1's integer.
func marshalGenerationDuration(value time.Duration) ([]byte, error) {
	return jsonv2.Marshal(int64(value))
}

func unmarshalGenerationDuration(data []byte, value *time.Duration) error {
	var nanoseconds int64
	if err := jsonv2.Unmarshal(data, &nanoseconds); err != nil {
		return err
	}
	*value = time.Duration(nanoseconds)
	return nil
}

func isGenerationHTTPOption(field string) bool {
	return field == generationHeadersField || field == generationTimeoutField
}

func validateGenerationHTTPOptions(options *genai.HTTPOptions) error {
	if options == nil {
		return nil
	}
	value := reflect.ValueOf(*options)
	for index := range value.NumField() {
		field := value.Type().Field(index)
		name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
		if isGenerationHTTPOption(name) || value.Field(index).IsZero() {
			continue
		}
		return fmt.Errorf("google: unsupported generation HTTP option %s", field.Name)
	}
	return nil
}
