package redis

import (
	"errors"
	"fmt"

	goredis "github.com/redis/go-redis/v9"
)

// RESP2 has no warning channel. Reading the complete RESP3 envelope also avoids
// the SDK's permissive decoder dropping malformed warnings or result members.
func parseSearchResult(raw any) (goredis.FTSearchResult, error) {
	var result goredis.FTSearchResult
	fields, err := responseFields(raw)
	if err != nil {
		return result, err
	}
	if fields["format"] != "STRING" {
		return result, errors.New("redis: FT.SEARCH requires the RESP3 STRING format")
	}
	total, ok := fields["total_results"].(int64)
	if !ok || total < 0 || int64(int(total)) != total {
		return result, errors.New("redis: FT.SEARCH has an invalid total_results")
	}
	warnings, ok := fields["warning"].([]any)
	if !ok {
		return result, errors.New("redis: FT.SEARCH is missing its warning channel")
	}
	if len(warnings) > 0 {
		return result, fmt.Errorf("redis: FT.SEARCH returned warnings: %v", warnings)
	}
	hits, ok := fields["results"].([]any)
	if !ok {
		return result, errors.New("redis: FT.SEARCH has invalid results")
	}
	result.Total = int(total)
	for _, rawHit := range hits {
		hit, err := responseFields(rawHit)
		if err != nil {
			return result, err
		}
		if failure, present := hit["error"]; present {
			return result, fmt.Errorf("redis: native hit failed: %v", failure)
		}
		id, ok := hit["id"].(string)
		if !ok || id == "" {
			return result, errors.New("redis: native hit has an invalid key")
		}
		attributes, err := responseFields(hit["extra_attributes"])
		if err != nil {
			return result, err
		}
		values := make(map[string]string, len(attributes))
		for key, rawValue := range attributes {
			value, ok := rawValue.(string)
			if !ok {
				return result, fmt.Errorf("redis: native field %q has type %T", key, rawValue)
			}
			values[key] = value
		}
		result.Docs = append(result.Docs, goredis.Document{ID: id, Fields: values})
	}
	return result, nil
}
