package workflow

import (
	"context"
	"encoding/json"
	"fmt"

	agent "github.com/Tangerg/scope/agent"
)

// fanoutSource supplies members and their inputs; window settlement and output
// handling belong to fanoutStage for both fixed branches and repeated items.
// Its unexported methods keep the Workflow operation set closed.
type fanoutSource interface {
	count(json.RawMessage) (uint32, error)
	windowInputs(json.RawMessage, uint32, uint32) ([]agent.Input, uint32, error)
	member(uint32) (fanoutMember, bool)
	topology(agent.Schema, agent.Schema) ([]BindingTopology, uint32)
}

type fanoutMember struct {
	id      string
	binding childBinding
}

type fanoutStage struct {
	source       fanoutSource
	windowSize   uint32
	outputSchema agent.Schema
	complete     func(context.Context, []json.RawMessage) (json.RawMessage, error)
}

func (s Stage) fanoutMemberLabel(index uint32) string {
	member, _ := s.fanout.source.member(index)
	return s.fanoutMemberNoun() + " " + member.id
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
