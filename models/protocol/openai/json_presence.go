package openai

import (
	"encoding/json/jsontext"

	"github.com/openai/openai-go/v3/packages/respjson"
)

// present reports an SDK member sent with a non-null value. Unlike
// respjson.Field.Valid it also reports a value of an unexpected type, because
// admission must reject such a member rather than treat it as absent.
func present(field respjson.Field) bool {
	raw := field.Raw()
	return raw != respjson.Omitted && raw != respjson.Null
}

// presentJSON reports a raw member that holds a value other than null.
func presentJSON(raw []byte) bool {
	value := jsontext.Value(raw)
	return len(value) != 0 && value.Kind() != jsontext.Null.Kind()
}

// falseJSON reports a raw member that holds the JSON literal false.
func falseJSON(raw []byte) bool {
	return jsontext.Value(raw).Kind() == jsontext.False.Kind()
}
