package protocol

import "encoding/json/jsontext"

// presentJSON reports a raw member that holds a value other than null.
func presentJSON(raw []byte) bool {
	value := jsontext.Value(raw)
	return len(value) != 0 && value.Kind() != jsontext.Null.Kind()
}
