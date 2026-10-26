package protocol

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"

	"google.golang.org/genai"

	"github.com/Tangerg/scope/core/metadata"
)

func decodeGenerationConfig(extensions metadata.Extensions, key string) (*genai.GenerateContentConfig, map[string]json.RawMessage, error) {
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
	return config, fields, nil
}
