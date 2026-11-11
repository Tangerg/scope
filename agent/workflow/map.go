package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"strconv"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

// MapConfig declares a bounded homogeneous item fan-out. The Stage input is
// []I, each exact child Program consumes one I and produces one O, and the
// Stage output is []O in original item order.
type MapConfig[I, O any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	Child run.ChildBinding

	// WindowSize is the positive number of items started and settled as one
	// execution window before the next window begins. Only that window is
	// decoded into I values; snapshot recovery still validates the full input.
	WindowSize uint32

	// MaxItems is the positive maximum accepted input length.
	MaxItems uint32
}

type mapSource struct {
	binding    run.ChildBinding
	codec      mapValueCodec
	decodeItem func(jsontext.Value) (run.Payload, error)
}

func (m mapSource) prepareInput(ctx context.Context, input run.Payload) (fanoutInput, error) {
	raw := input.JSON()
	spans, err := m.codec.index(ctx, raw)
	if err != nil {
		return nil, errors.Join(ErrInvalidExecutionState, err)
	}
	return mapInputIndex{raw: raw, spans: spans, decodeItem: m.decodeItem, stageID: m.codec.id}, nil
}

func (m mapSource) member(index uint32) (fanoutMember, bool) {
	return fanoutMember{id: strconv.FormatUint(uint64(index), 10), binding: m.binding}, true
}

func (m mapSource) bindings() []run.ChildBinding { return []run.ChildBinding{m.binding} }

func (m mapSource) topology(_ jsonschema.Schema, outputSchema jsonschema.Schema) ([]BindingTopology, uint32) {
	return []BindingTopology{bindingTopology(m.binding, BindingRoleItem, "", m.codec.schemas.itemInput, outputSchema)}, m.codec.maxItems
}

// The immutable stage input owns item bytes. Offsets are a disposable index;
// only the requested window is decoded into typed child inputs.
type mapInputIndex struct {
	raw        json.RawMessage
	spans      []mapItemSpan
	decodeItem func(jsontext.Value) (run.Payload, error)
	stageID    string
}

func (m mapInputIndex) count() uint32 { return uint32(len(m.spans)) }

func (m mapInputIndex) windowInputs(ctx context.Context, start, windowSize uint32) ([]run.Payload, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	count := m.count()
	if start > count {
		return nil, fmt.Errorf("%w: window start %d exceeds item count %d", ErrInvalidExecutionState, start, count)
	}
	end := start + fanoutWindowLen(start, count, windowSize)
	var inputs []run.Payload
	if end > start {
		inputs = make([]run.Payload, 0, end-start)
	}
	for index := start; index < end; index++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		span := m.spans[index]
		input, err := m.decodeItem(jsontext.Value(m.raw[span.start:span.end]))
		if err != nil {
			return nil, fmt.Errorf("%w: Map %q item %d: %w", ErrInvalidExecutionState, m.stageID, index, err)
		}
		inputs = append(inputs, input)
	}
	return inputs, nil
}

type mapItemSpan struct{ start, end int }

// Map constructs one bounded managed item fan-out Stage. Empty input is valid
// and produces a non-nil empty []O without creating child Processes.
func Map[I, O any](config MapConfig[I, O]) (Stage, error) {
	binding := config.Child
	if !run.ValidQualifiedName(config.ID) || !binding.Valid() ||
		config.WindowSize == 0 || config.MaxItems == 0 || config.WindowSize > config.MaxItems {
		return Stage{}, ErrInvalidStage
	}
	schemas, err := mapSchemasFor[I, O](config.ID)
	if err != nil {
		return Stage{}, err
	}
	descriptor := binding.Program.Descriptor()
	if !schemas.itemInput.Equal(descriptor.InputSchema()) ||
		!schemas.itemOutput.Equal(descriptor.OutputSchema()) {
		return Stage{}, fmt.Errorf("%w: Map %q child schema mismatch", ErrInvalidStage, config.ID)
	}
	codec := mapValueCodec{id: config.ID, maxItems: config.MaxItems, schemas: schemas}
	collect := func(ctx context.Context, _ run.Payload, itemOutputs []run.Payload) (run.Payload, error) {
		return codec.collect[O](ctx, itemOutputs)
	}
	return Stage{
		id: config.ID, kind: StageKindMap,
		inputSchema: schemas.input, outputSchema: schemas.output,
		fanout: fanoutStage{
			source:     mapSource{binding: binding, codec: codec, decodeItem: codec.item[I]},
			windowSize: config.WindowSize, memberOutputSchema: schemas.itemOutput, complete: collect,
		},
	}, nil
}

type mapSchemas struct {
	input      jsonschema.Schema
	itemInput  jsonschema.Schema
	itemOutput jsonschema.Schema
	output     jsonschema.Schema
}

func mapSchemasFor[I, O any](id string) (mapSchemas, error) {
	input, err := jsonschema.For[[]I]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q input schema: %w", ErrInvalidStage, id, err)
	}
	itemInput, err := jsonschema.For[I]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q item schema: %w", ErrInvalidStage, id, err)
	}
	itemOutput, err := jsonschema.For[O]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q item output schema: %w", ErrInvalidStage, id, err)
	}
	output, err := jsonschema.For[[]O]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q output schema: %w", ErrInvalidStage, id, err)
	}
	return mapSchemas{input: input, itemInput: itemInput, itemOutput: itemOutput, output: output}, nil
}

type mapValueCodec struct {
	id       string
	maxItems uint32
	schemas  mapSchemas
}

// index reads the canonical input array once and enforces the item limit
// before reading another value. Decoder offsets refer to the immutable input,
// so later windows need neither rescanning nor a second copy of item bytes.
func (m mapValueCodec) index(ctx context.Context, raw json.RawMessage) ([]mapItemSpan, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.ReadToken()
	if err != nil || token.Kind() != '[' {
		return nil, errors.Join(ErrInvalidExecutionState, err)
	}
	var spans []mapItemSpan
	for decoder.PeekKind() != ']' {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if uint64(len(spans)) == uint64(m.maxItems) {
			return nil, mapMaxItemsExceededError{count: uint64(len(spans)) + 1, maximum: m.maxItems}
		}
		value, err := decoder.ReadValue()
		if err != nil {
			return nil, err
		}
		end := int(decoder.InputOffset())
		spans = append(spans, mapItemSpan{start: end - len(value), end: end})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if _, err := decoder.ReadToken(); err != nil {
		return nil, err
	}
	return spans, nil
}

// Each child receives EncodePayload(I), including any typed codec behavior.
// Forwarding the raw item would bypass that contract even for canonical JSON.
// The item is a span of the canonical stage input, so it decodes in place.
func (m mapValueCodec) item[I any](raw jsontext.Value) (run.Payload, error) {
	value, err := jsonwire.Decode[I](raw)
	if err != nil {
		return run.Payload{}, err
	}
	item, err := run.EncodePayload(value)
	if err != nil {
		return run.Payload{}, err
	}
	if err := m.schemas.itemInput.Validate(item.JSON()); err != nil {
		return run.Payload{}, err
	}
	return item, nil
}

func (m mapValueCodec) collect[O any](ctx context.Context, itemOutputs []run.Payload) (run.Payload, error) {
	outputs := fanoutOutputs{kind: StageKindMap, stageID: m.id, resultSchema: m.schemas.output}
	values, err := outputs.decode[O](ctx, itemOutputs)
	if err != nil {
		return run.Payload{}, err
	}
	return outputs.encodeResult(values)
}

type mapMaxItemsExceededError struct {
	count   uint64
	maximum uint32
}

func (m mapMaxItemsExceededError) Error() string {
	return fmt.Sprintf("Map input contains at least %d items, exceeding maximum %d", m.count, m.maximum)
}
