package workflow

import (
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"slices"
	"testing"
)

func TestMapScanSelectsOnlyTheRequestedWindow(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 4}
	var selected []string
	count, err := codec.scan(json.RawMessage(`[1,{"nested":[2,3]},"four",null]`), 1, 3, func(value jsontext.Value) error {
		selected = append(selected, string(value))
		return nil
	})
	if err != nil || count != 4 || !slices.Equal(selected, []string{`{"nested":[2,3]}`, `"four"`}) {
		t.Fatalf("count=%d selected=%v error=%v", count, selected, err)
	}
}

func TestMapScanStopsAtTheItemLimitBeforeReadingAnotherValue(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 2}
	_, err := codec.scan(json.RawMessage(`[1,2,{"unread":`), 0, 0, nil)
	exceeded, ok := errors.AsType[mapMaxItemsExceededError](err)
	if !ok || exceeded.count != 3 || exceeded.maximum != 2 {
		t.Fatalf("error=%v", err)
	}
}

func TestMapScanRejectsInvalidArrayStructure(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 4}
	for _, raw := range []string{`null`, `{}`, `[`, `[1,]`, `[{"x":1,"x":2}]`, `[] true`, `["\xff"]`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := codec.scan(json.RawMessage(raw), 0, 0, nil); err == nil {
				t.Fatal("invalid array was accepted")
			}
		})
	}
}
