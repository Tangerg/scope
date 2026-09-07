package workflow

import (
	"encoding/json"
	"fmt"
	"strconv"

	agent "github.com/Tangerg/scope/agent"
)

func (s Stage) fanoutCount(raw json.RawMessage) (uint32, error) {
	switch s.kind {
	case StageKindFork:
		if !s.fork.valid() {
			return 0, ErrInvalidStage
		}
		return uint32(len(s.fork.branches)), nil
	case StageKindMap:
		if !s.mapper.valid() {
			return 0, ErrInvalidStage
		}
		return s.mapper.count(raw)
	default:
		return 0, ErrInvalidStage
	}
}

func (s Stage) fanoutWindowSize() uint32 {
	switch s.kind {
	case StageKindFork:
		return s.fork.windowSize
	case StageKindMap:
		return s.mapper.windowSize
	default:
		return 0
	}
}

func (s Stage) fanoutBinding(index uint32) (childBinding, bool) {
	switch s.kind {
	case StageKindFork:
		if uint64(index) >= uint64(len(s.fork.branches)) {
			return childBinding{}, false
		}
		return s.fork.branches[index].binding, true
	case StageKindMap:
		return s.mapper.binding, s.mapper.valid()
	default:
		return childBinding{}, false
	}
}

func (s Stage) fanoutWindowInputs(
	start uint32,
	end uint32,
	raw json.RawMessage,
) ([]agent.Input, error) {
	switch s.kind {
	case StageKindFork:
		input, err := agent.ParseInput(raw)
		if err != nil {
			return nil, err
		}
		inputs := make([]agent.Input, end-start)
		for index := range inputs {
			inputs[index] = input
		}
		return inputs, nil
	case StageKindMap:
		return s.mapper.windowInputs(raw, start, end)
	default:
		return nil, ErrInvalidStage
	}
}

func (s Stage) fanoutOutputSchema() agent.Schema {
	switch s.kind {
	case StageKindFork:
		return s.fork.branchSchema
	case StageKindMap:
		return s.mapper.itemOutputSchema
	default:
		return agent.Schema{}
	}
}

func (s Stage) fanoutComplete(outputs []json.RawMessage) (json.RawMessage, error) {
	switch s.kind {
	case StageKindFork:
		return s.fork.reduce(outputs)
	case StageKindMap:
		return s.mapper.collect(outputs)
	default:
		return nil, ErrInvalidStage
	}
}

func (s Stage) fanoutMemberID(index uint32) (string, bool) {
	switch s.kind {
	case StageKindFork:
		if uint64(index) >= uint64(len(s.fork.branches)) {
			return "", false
		}
		return s.fork.branches[index].id, true
	case StageKindMap:
		return strconv.FormatUint(uint64(index), 10), true
	default:
		return "", false
	}
}

func (s Stage) fanoutMemberLabel(index uint32) string {
	id, _ := s.fanoutMemberID(index)
	if s.kind == StageKindFork {
		return "branch " + id
	}
	return "item " + id
}

func (s Stage) fanoutFailureCode(suffix string) string {
	return s.failureCode(s.fanoutMemberNoun() + "_" + suffix)
}

func (s Stage) failureCode(suffix string) string {
	return fmt.Sprintf("workflow.%s.%s", string(s.kind), suffix)
}

func (s Stage) fanoutMemberNoun() string {
	if s.kind == StageKindFork {
		return "branch"
	}
	return "item"
}

type fanoutOutputDecoder struct {
	stageName  string
	stageID    string
	memberName string
	schema     agent.Schema
}

func (f fanoutOutputDecoder) decode[T any](encodedOutputs []json.RawMessage) ([]T, error) {
	values := make([]T, len(encodedOutputs))
	for index, encoded := range encodedOutputs {
		output, err := agent.ParseOutput(encoded)
		if err != nil {
			return nil, fmt.Errorf("%s %q %s %d output: %w", f.stageName, f.stageID, f.memberName, index, err)
		}
		if validateOutputErr := f.schema.ValidateOutput(output); validateOutputErr != nil {
			return nil, fmt.Errorf("%s %q %s %d output contract: %w", f.stageName, f.stageID, f.memberName, index, validateOutputErr)
		}
		decoded, err := output.Decode[T]()
		if err != nil {
			return nil, fmt.Errorf("%s %q %s %d decode output: %w", f.stageName, f.stageID, f.memberName, index, err)
		}
		values[index] = decoded
	}
	return values, nil
}
