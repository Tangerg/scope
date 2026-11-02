package protocol

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
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
	if err := jsonv2.Unmarshal(raw, config, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, nil, fmt.Errorf("google: extension %q: %w", key, err)
	}
	// The SDK omits a zero count; missing and null also select its single-candidate default.
	if count := config.CandidateCount; count < 0 || count > 1 {
		return nil, nil, fmt.Errorf("google: extension %q requests %d candidates for a single-output generation", key, count)
	}
	if rawTools, found := fields["tools"]; found {
		var tools []map[string]json.RawMessage
		if err := jsonv2.Unmarshal(rawTools, &tools); err != nil {
			return nil, nil, fmt.Errorf("google: extension %q field tools: %w", key, err)
		}
		for index, tool := range tools {
			if _, found := tool["codeExecution"]; found {
				return nil, nil, fmt.Errorf("google: extension %q field tools[%d].codeExecution: %w: code execution output and replay cannot be represented", key, index, errors.ErrUnsupported)
			}
		}
	}
	return config, fields, nil
}
