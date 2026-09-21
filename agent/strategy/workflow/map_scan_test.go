package workflow

import (
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"math"
	"slices"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestFanoutSourcesOwnWindowBounds(t *testing.T) {
	for name, source := range map[string]fanoutSource{
		"map": mapSource{
			codec:      mapValueCodec{id: "items", maxItems: 4},
			decodeItem: func(value jsontext.Value) (agent.Payload, error) { return agent.ParsePayload(value) },
		},
		"fork": forkSource{branches: make([]fanoutMember, 2)},
	} {
		t.Run(name, func(t *testing.T) {
			for _, start := range []uint32{3, 5, math.MaxUint32} {
				if inputs, _, err := source.windowInputs(t.Context(), json.RawMessage(`[1,2]`), start, math.MaxUint32); !errors.Is(err, ErrInvalidExecutionState) || len(inputs) != 0 {
					t.Fatalf("start %d: inputs=%v error=%v", start, inputs, err)
				}
			}
			for _, start := range []uint32{0, 1, 2} {
				inputs, count, err := source.windowInputs(t.Context(), json.RawMessage(`[1,2]`), start, math.MaxUint32)
				if err != nil || count != 2 || len(inputs) != int(2-start) {
					t.Fatalf("start %d: inputs=%v count=%d error=%v", start, inputs, count, err)
				}
			}
		})
	}
}

func TestMapScanSelectsOnlyTheRequestedWindow(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 4}
	var selected []string
	count, err := codec.scan(t.Context(), json.RawMessage(`[1,{"nested":[2,3]},"four",null]`), 1, 3, func(value jsontext.Value) error {
		selected = append(selected, string(value))
		return nil
	})
	if err != nil || count != 4 || !slices.Equal(selected, []string{`{"nested":[2,3]}`, `"four"`}) {
		t.Fatalf("count=%d selected=%v error=%v", count, selected, err)
	}
}

func TestMapScanStopsAtTheItemLimitBeforeReadingAnotherValue(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 2}
	_, err := codec.scan(t.Context(), json.RawMessage(`[1,2,{"unread":`), 0, 0, nil)
	exceeded, ok := errors.AsType[mapMaxItemsExceededError](err)
	if !ok || exceeded.count != 3 || exceeded.maximum != 2 {
		t.Fatalf("error=%v", err)
	}
}

func TestMapScanRejectsInvalidArrayStructure(t *testing.T) {
	codec := mapValueCodec{id: "items", maxItems: 4}
	for _, raw := range []string{`null`, `{}`, `[`, `[1,]`, `[{"x":1,"x":2}]`, `[] true`, `["\xff"]`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := codec.scan(t.Context(), json.RawMessage(raw), 0, 0, nil); err == nil {
				t.Fatal("invalid array was accepted")
			}
		})
	}
}

func TestMapWindowStopsAfterCanceledDecode(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	calls := 0
	source := mapSource{codec: mapValueCodec{id: "items", maxItems: 2}, decodeItem: func(value jsontext.Value) (agent.Payload, error) {
		calls++
		cancel()
		return agent.ParsePayload(value)
	}}
	_, _, err := source.windowInputs(ctx, json.RawMessage(`[1,2]`), 0, 2)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("error=%v calls=%d", err, calls)
	}
}

func TestMapCountStopsBeforeScanningLaterInvalidItem(t *testing.T) {
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	source := mapSource{codec: mapValueCodec{id: "items", maxItems: 3}}
	if _, err := source.count(ctx, json.RawMessage(`[1,{"invalid":]`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("scan continued after cancellation: %v", err)
	}
}

func TestFanoutOutputDecodeStopsBetweenItems(t *testing.T) {
	schema, err := agent.SchemaFor[int]()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Map", "Fork"} {
		t.Run(kind, func(t *testing.T) {
			ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
			defer cancel()
			decoder := fanoutOutputDecoder{stageName: kind, stageID: "items", memberName: "item", schema: schema}
			values, err := decoder.decode[int](ctx, []json.RawMessage{json.RawMessage(`1`), json.RawMessage(`"invalid"`)})
			if values != nil || !errors.Is(err, context.Canceled) {
				t.Fatalf("decode continued after cancellation: %v %v", values, err)
			}
		})
	}
}
