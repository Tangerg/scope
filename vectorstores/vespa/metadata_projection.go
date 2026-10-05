package vespa

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"fmt"
	"slices"

	"github.com/Tangerg/scope/core/metadata"
)

func projectMetadata(attributes metadata.Map) (string, []string, error) {
	if attributes == nil {
		attributes = metadata.Map{}
	}
	encoded, err := jsonv2.Marshal(attributes)
	if err != nil {
		return "", nil, fmt.Errorf("vespa: encode metadata: %w", err)
	}
	paths, err := metadataPaths(attributes, nil)
	if err != nil {
		return "", nil, err
	}
	slices.Sort(paths)
	return string(encoded), paths, nil
}

func metadataPaths(attributes map[string]json.RawMessage, prefix []string) ([]string, error) {
	paths := make([]string, 0, len(attributes))
	for key, value := range attributes {
		raw := bytes.TrimSpace(value)
		if bytes.Equal(raw, []byte("null")) {
			continue
		}
		path := append(slices.Clone(prefix), key)
		encoded, err := encodeMetadataPath(path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, encoded)
		if raw[0] != '{' {
			continue
		}
		var nested map[string]json.RawMessage
		if err = jsonv2.Unmarshal(raw, &nested); err != nil {
			return nil, fmt.Errorf("vespa: decode metadata at %s: %w", encoded, err)
		}
		descendants, err := metadataPaths(nested, path)
		if err != nil {
			return nil, err
		}
		paths = append(paths, descendants...)
	}
	return paths, nil
}

func encodeMetadataPath(keys []string) (string, error) {
	encoded, err := jsonv2.Marshal(keys)
	if err != nil {
		return "", fmt.Errorf("vespa: encode metadata path: %w", err)
	}
	return string(encoded), nil
}
