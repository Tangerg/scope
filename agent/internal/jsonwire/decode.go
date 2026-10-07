package jsonwire

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
)

// requiredTag marks a struct member that must be present and non-null. Its
// zero value is a meaningful fact, so a missing member must not silently
// supply it. The declaration owns the rule; callers never restate the names.
const requiredTag = "required"

// Decode strictly decodes data into T: unknown and duplicate members are
// rejected, and every member tagged `jsonwire:"required"` must be present and
// non-null.
func Decode[T any](data []byte) (T, error) {
	var value T
	if len(data) == 0 {
		return value, errors.New("JSON value is empty")
	}
	if required := requiredMembers(reflect.TypeFor[T]()); len(required) != 0 {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(data, &fields); err != nil {
			return value, err
		}
		for _, name := range required {
			field, present := fields[name]
			if !present || bytes.Equal(bytes.TrimSpace(field), []byte("null")) {
				return value, fmt.Errorf("required JSON member %q is missing or null", name)
			}
		}
	}
	if err := jsonv2.Unmarshal(data, &value, jsonv2.RejectUnknownMembers(true)); err != nil {
		return value, err
	}
	return value, nil
}

var requiredByType sync.Map

// requiredMembers lists the JSON names of t's required members, including
// those of embedded structs whose members JSON inlines.
func requiredMembers(t reflect.Type) []string {
	if cached, found := requiredByType.Load(t); found {
		return cached.([]string)
	}
	var names []string
	if t.Kind() == reflect.Struct {
		for field := range t.Fields() {
			name, _, _ := strings.Cut(field.Tag.Get("json"), ",")
			if field.Anonymous && name == "" && field.Type.Kind() == reflect.Struct {
				names = append(names, requiredMembers(field.Type)...)
				continue
			}
			if field.Tag.Get("jsonwire") == requiredTag {
				names = append(names, name)
			}
		}
	}
	cached, _ := requiredByType.LoadOrStore(t, names)
	return cached.([]string)
}
