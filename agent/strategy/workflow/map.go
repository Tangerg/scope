package workflow

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/json/jsontext"
	"errors"
	"fmt"
	"io"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
)

// MapConfig declares a bounded homogeneous item fan-out. The Stage input is
// []I, each exact child Deployment consumes one I and produces one O, and the
// Stage output is []O in original item order.
type MapConfig[I, O any] struct {
	// ID is unique within the Workflow and remains stable across restoration.
	ID string

	// Deployment is the exact child behavior binding used for every item.
	Deployment agent.Deployment

	// Budget is permanently allocated from the parent for each started item.
	Budget agent.Budget

	// Capabilities is the attenuated authority set granted to each child.
	Capabilities agent.CapabilitySet

	// WindowSize is the positive number of items started and settled as one
	// execution window before the next window begins. Only that window is
	// decoded into I values; snapshot recovery still validates the full input.
	WindowSize uint32

	// MaxItems is the positive maximum accepted input length.
	MaxItems uint32
}

type mapSource struct {
	binding    childBinding
	codec      mapValueCodec
	decodeItem func(jsontext.Value) (agent.Input, error)
}

func (m mapSource) count(raw json.RawMessage) (uint32, error) {
	return m.codec.scan(raw, 0, 0, nil)
}

func (m mapSource) windowInputs(raw json.RawMessage, start, windowSize uint32) ([]agent.Input, uint32, error) {
	if start > m.codec.maxItems {
		return nil, 0, ErrInvalidExecutionState
	}
	end := start + min(windowSize, m.codec.maxItems-start)
	var items []agent.Input
	count, err := m.codec.scan(raw, start, end, func(value jsontext.Value) error {
		input, err := m.decodeItem(value)
		if err != nil {
			return err
		}
		items = append(items, input)
		return nil
	})
	if err != nil {
		return nil, 0, errors.Join(ErrInvalidExecutionState, err)
	}
	return items, count, nil
}

func (m mapSource) member(index uint32) (fanoutMember, bool) {
	return fanoutMember{id: strconv.FormatUint(uint64(index), 10), binding: m.binding}, true
}

func (m mapSource) topology(_ agent.Schema, outputSchema agent.Schema) ([]BindingTopology, uint32) {
	return []BindingTopology{m.binding.topology(BindingRoleItem, "", m.codec.schemas.itemInput, outputSchema)}, m.codec.maxItems
}

// Map constructs one bounded managed item fan-out Stage. Empty input is valid
// and produces a non-nil empty []O without creating child Processes.
func Map[I, O any](config MapConfig[I, O]) (Stage, error) {
	if !validStageID(config.ID) || !config.Deployment.Valid() ||
		!config.Budget.Valid() || !config.Capabilities.Valid() ||
		config.WindowSize == 0 || config.MaxItems == 0 || config.WindowSize > config.MaxItems {
		return Stage{}, ErrInvalidStage
	}
	schemas, err := mapSchemasFor[I, O](config.ID)
	if err != nil {
		return Stage{}, err
	}
	descriptor := config.Deployment.Descriptor()
	if !schemasEqual(schemas.itemInput, descriptor.InputSchema()) ||
		!schemasEqual(schemas.itemOutput, descriptor.OutputSchema()) {
		return Stage{}, fmt.Errorf("%w: Map %q child schema mismatch", ErrInvalidStage, config.ID)
	}
	codec := mapValueCodec{id: config.ID, maxItems: config.MaxItems, schemas: schemas}
	collect := func(_ context.Context, raw []json.RawMessage) (json.RawMessage, error) {
		return codec.collect[O](raw)
	}
	return Stage{
		id: config.ID, kind: StageKindMap,
		inputSchema: schemas.input, outputSchema: schemas.output,
		fanout: fanoutStage{
			source: mapSource{
				binding: childBinding{
					deploymentRef: config.Deployment.DeploymentRef(), budget: config.Budget, capabilities: config.Capabilities,
				},
				codec: codec, decodeItem: codec.item[I],
			},
			windowSize: config.WindowSize, outputSchema: schemas.itemOutput, complete: collect,
		},
	}, nil
}

type mapSchemas struct {
	input      agent.Schema
	itemInput  agent.Schema
	itemOutput agent.Schema
	output     agent.Schema
}

func mapSchemasFor[I, O any](id string) (mapSchemas, error) {
	input, err := agent.SchemaFor[[]I]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q input schema: %w", ErrInvalidStage, id, err)
	}
	itemInput, err := agent.SchemaFor[I]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q item schema: %w", ErrInvalidStage, id, err)
	}
	itemOutput, err := agent.SchemaFor[O]()
	if err != nil {
		return mapSchemas{}, fmt.Errorf("%w: Map %q item output schema: %w", ErrInvalidStage, id, err)
	}
	output, err := agent.SchemaFor[[]O]()
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

// scan enforces the item limit before materializing the next value. Values
// outside the selected window are checked structurally without typed decoding.
func (m mapValueCodec) scan(
	raw json.RawMessage,
	start, end uint32,
	consume func(jsontext.Value) error,
) (uint32, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.ReadToken()
	if err != nil || token.Kind() != '[' {
		return 0, errors.Join(ErrInvalidExecutionState, err)
	}
	var count uint32
	for decoder.PeekKind() != ']' {
		if count == m.maxItems {
			return 0, mapMaxItemsExceededError{count: uint64(count) + 1, maximum: m.maxItems}
		}
		if count >= start && count < end {
			value, err := decoder.ReadValue()
			if err != nil {
				return 0, err
			}
			if err := consume(value); err != nil {
				return 0, fmt.Errorf("Map %q item %d: %w", m.id, count, err)
			}
		} else if err := decoder.SkipValue(); err != nil {
			return 0, err
		}
		count++
	}
	if _, err := decoder.ReadToken(); err != nil {
		return 0, err
	}
	if _, err := decoder.ReadToken(); !errors.Is(err, io.EOF) {
		return 0, errors.Join(ErrInvalidExecutionState, err)
	}
	return count, nil
}

func (m mapValueCodec) item[I any](raw jsontext.Value) (agent.Input, error) {
	input, err := agent.ParseInput(json.RawMessage(raw))
	if err != nil {
		return agent.Input{}, err
	}
	value, err := input.Decode[I]()
	if err != nil {
		return agent.Input{}, err
	}
	item, err := agent.EncodeInput(value)
	if err != nil {
		return agent.Input{}, err
	}
	if err := m.schemas.itemInput.ValidateInput(item); err != nil {
		return agent.Input{}, err
	}
	return item, nil
}

func (m mapValueCodec) collect[O any](raw []json.RawMessage) (json.RawMessage, error) {
	decoder := fanoutOutputDecoder{
		stageName: "Map", stageID: m.id, memberName: "item", schema: m.schemas.itemOutput,
	}
	values, err := decoder.decode[O](raw)
	if err != nil {
		return nil, err
	}
	erased, err := agent.EncodeOutput(values)
	if err != nil {
		return nil, fmt.Errorf("Map %q encode result: %w", m.id, err)
	}
	if err := m.schemas.output.ValidateOutput(erased); err != nil {
		return nil, fmt.Errorf("Map %q result contract: %w", m.id, err)
	}
	return erased.JSON(), nil
}

type mapMaxItemsExceededError struct {
	count   uint64
	maximum uint32
}

func (m mapMaxItemsExceededError) Error() string {
	return fmt.Sprintf("Map input contains at least %d items, exceeding maximum %d", m.count, m.maximum)
}
