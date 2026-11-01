package openai

import (
	"strconv"

	"github.com/openai/openai-go/v3/packages/respjson"
)

// The SDK's permissive decoder may coerce quoted or fractional numbers. An
// ordinal binds provider output to a caller input or call, so zero cannot be a
// fallback for a missing or malformed index.
func responseIndex(field respjson.Field) (int64, bool) {
	i, err := strconv.ParseInt(field.Raw(), 10, 0)
	return i, err == nil && i >= 0
}
