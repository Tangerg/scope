package workflow

import (
	"context"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
)

func TestMapInputWindowsDecodeOnlyTheirItems(t *testing.T) {
	var decoded []string
	source := mapSource{codec: mapValueCodec{id: "test.window", maxItems: 4}, decodeItem: func(value jsontext.Value) (run.Payload, error) {
		decoded = append(decoded, string(value))
		return run.ParsePayload(value)
	}}
	raw := []byte("[ \n {\"value\": [1, 2]}, \"逗号,\\\"\", true , null ]")
	input, err := source.prepareInput(t.Context(), raw)
	if err != nil || input.count() != 4 || len(decoded) != 0 {
		t.Fatalf("input validation decoded items: decoded=%v error=%v", decoded, err)
	}
	for _, sample := range []struct {
		start uint32
		size  uint32
		want  []string
	}{
		{1, 2, []string{`"逗号,\""`, `true`}},
		{3, 2, []string{`null`}},
		{0, 1, []string{`{"value": [1, 2]}`}},
		{4, 1, nil},
	} {
		decoded = nil
		values, err := input.windowInputs(t.Context(), sample.start, sample.size)
		if err != nil || len(values) != len(sample.want) || !slices.Equal(decoded, sample.want) {
			t.Fatalf("window at %d: decoded=%v error=%v", sample.start, decoded, err)
		}
		for index, value := range values {
			expected, err := run.ParsePayload([]byte(sample.want[index]))
			if err != nil || !value.Equal(expected) {
				t.Fatalf("window changed item %d: %v", sample.start+uint32(index), err)
			}
		}
	}
	if _, err := input.windowInputs(t.Context(), 5, 1); !errors.Is(err, ErrInvalidExecutionState) {
		t.Fatalf("window beyond input was accepted: %v", err)
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := input.windowInputs(canceled, 0, 1); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled window decoded an item: %v", err)
	}
}

func TestMapInputRejectsMalformedOrExcessiveArraysBeforeDecoding(t *testing.T) {
	source := mapSource{codec: mapValueCodec{id: "test.input", maxItems: 2}, decodeItem: func(jsontext.Value) (run.Payload, error) {
		t.Fatal("input validation decoded a typed item")
		return run.Payload{}, nil
	}}
	for _, raw := range []string{`null`, `{}`, `[1,]`, `[1`, `[1,2] true`, `[{"a":1,"a":2}]`, `[1,2,3]`} {
		if _, err := source.prepareInput(t.Context(), []byte(raw)); !errors.Is(err, ErrInvalidExecutionState) {
			t.Fatalf("invalid array %s: %v", raw, err)
		}
	}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.prepareInput(canceled, []byte(`[1]`)); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled indexing succeeded: %v", err)
	}
}

func TestConsecutiveMapsDeriveWindowsFromTheirOwnStageInput(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	transform := mustTransform(t, "increment", func(_ context.Context, value int) (int, error) { return value + 1, nil })
	leaf, err := Compile[int, int](runtime.Compiler(), Config{Name: "test.map_leaf", Description: "Increment one item.", Revision: "increment", Stages: []Stage{transform}})
	if err != nil {
		t.Fatal(err)
	}
	var stages []Stage
	for _, id := range []string{"first", "second"} {
		stage, stageErr := Map(MapConfig[int, int]{ID: id, Child: run.ChildBinding{Program: leaf.Erase()}, WindowSize: 1, MaxItems: 3})
		if stageErr != nil {
			t.Fatal(stageErr)
		}
		stages = append(stages, stage)
	}
	program, err := Compile[[]int, []int](runtime.Compiler(), Config{Name: "test.consecutive_maps", Description: "Apply two bounded maps.", Stages: stages})
	if err != nil {
		t.Fatal(err)
	}
	for _, input := range [][]int{{1, 2, 3}, {8, 9}} {
		output, err := runtime.Run(t.Context(), program, input)
		if err != nil {
			t.Fatal(err)
		}
		if len(output) != len(input) {
			t.Fatalf("map changed the item count: input=%v output=%v", input, output)
		}
		for index, value := range output {
			if value != input[index]+2 {
				t.Fatalf("map reused another stage or execution's input: input=%v output=%v", input, output)
			}
		}
	}
}

func BenchmarkMapAllWindows(b *testing.B) {
	for _, count := range []uint32{1000, 2000, 4000} {
		b.Run(fmt.Sprintf("items_%d_window_1", count), func(b *testing.B) {
			raw := []byte("[" + strings.Repeat("0,", int(count)-1) + "0]")
			source := mapSource{codec: mapValueCodec{id: "benchmark", maxItems: count}, decodeItem: func(value jsontext.Value) (run.Payload, error) { return run.ParsePayload(value) }}
			b.ReportAllocs()
			for b.Loop() {
				input, err := source.prepareInput(b.Context(), raw)
				if err != nil || input.count() != count {
					b.Fatalf("invalid input: %v", err)
				}
				for start := uint32(0); start < count; start++ {
					values, err := input.windowInputs(b.Context(), start, 1)
					if err != nil || len(values) != 1 {
						b.Fatalf("invalid window: %v", err)
					}
				}
			}
		})
	}
}
