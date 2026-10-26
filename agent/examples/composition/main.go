// Command composition demonstrates that direct Engine embedding and a
// cross-Strategy Agent composed from Workflow and Role atoms use the same
// Definition/Execution/Process contracts. It requires no network.
package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/agent/strategy/role"
	"github.com/Tangerg/scope/agent/strategy/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
)

const (
	compositionChildBudgetSteps    = 20
	compositionChildBudgetEffects  = 20
	compositionChildBudgetSignals  = 40
	compositionBranchBudgetSteps   = 40
	compositionBranchBudgetEffects = 40
	compositionBranchBudgetSignals = 80
)

const (
	compositionLocalIndex = iota
	compositionModelIndex
)

// uppercaseStateKind names the Definition and its ExecutionState kind.
const uppercaseStateKind = "example.uppercase"

const compositionName = "example.composition"

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	localDeployment, err := newUppercaseDeployment()
	if err != nil {
		return err
	}
	modelDeployment, err := newModelDeployment()
	if err != nil {
		return err
	}
	compositionDeployment, err := newCompositionDeployment(localDeployment, modelDeployment)
	if err != nil {
		return err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: agent.NewMemoryTreeCommitter()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, engine.Close(context.WithoutCancel(ctx))) }()

	embeddedInput, err := localDeployment.Descriptor().EncodeInput(textInput{Text: "embedded"})
	if err != nil {
		return err
	}
	embeddedResult, err := engine.Run(ctx, localDeployment, embeddedInput)
	if err != nil {
		return err
	}
	embedded, err := decodeCompleted[textOutput](embeddedResult)
	if err != nil {
		return err
	}

	composedInput, err := compositionDeployment.Descriptor().EncodeInput(compositionInput{Prompt: "composition"})
	if err != nil {
		return err
	}
	compositionResult, err := engine.Run(ctx, compositionDeployment, composedInput)
	if err != nil {
		return err
	}
	composed, err := decodeCompleted[compositionOutput](compositionResult)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(
		output, "embedded: %s\ncomposed: %s | %s\n",
		embedded.Text, composed.Local, composed.Model,
	)
	return err
}

type textInput struct {
	Text string `json:"text"`
}

type textOutput struct {
	Text string `json:"text"`
}

type uppercaseDefinition struct{ descriptor agent.Descriptor }

func newUppercaseDeployment() (agent.Deployment, error) {
	inputSchema, err := jsonschema.For[textInput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	outputSchema, err := jsonschema.For[textOutput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: uppercaseStateKind, Description: "Return input text in uppercase.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           &uppercaseDefinition{descriptor: descriptor},
		ImplementationDigest: agent.ComputeDigest([]byte("example-uppercase-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-uppercase-configuration")),
	})
}

func (u *uppercaseDefinition) Descriptor() agent.Descriptor { return u.descriptor }

func (*uppercaseDefinition) ChildDeployments() []agent.Deployment { return nil }

func (u *uppercaseDefinition) Start(input agent.Payload) (agent.Execution, error) {
	if err := u.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	decoded, err := input.Decode[textInput]()
	if err != nil {
		return nil, err
	}
	return &uppercaseExecution{Text: decoded.Text}, nil
}

func (*uppercaseDefinition) Restore(ctx context.Context, state agent.ExecutionState) (agent.Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	execution, err := state.Decode[uppercaseExecution](uppercaseStateKind)
	if err != nil {
		return nil, err
	}
	return &execution, nil
}

type uppercaseExecution struct {
	Text string `json:"text"`
	Done bool   `json:"done"`
}

func (u *uppercaseExecution) Step(_ context.Context, signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != 0 {
		return agent.Transition{}, agent.ErrInvalidSignal
	}
	if u.Done {
		return agent.Transition{}, errors.New("uppercase execution already completed")
	}
	u.Done = true
	value, err := agent.EncodePayload(textOutput{Text: strings.ToUpper(u.Text)})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, value)
}

func (u *uppercaseExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := jsonv2.Marshal(u)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.ParseExecutionState(uppercaseStateKind, payload)
}

func newModelDeployment() (agent.Deployment, error) {
	client, err := chatclient.New(compositionModel{}, chatclient.Config{})
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.composition_model", Description: "Return one deterministic composition response.",
		MaxModelCalls: agent.NewQuota(1),
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: client})
	if err != nil {
		return agent.Deployment{}, err
	}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("example-composition-model-implementation")),
		ConfigurationDigest:  agent.ComputeDigest([]byte("example-composition-model-configuration")),
	})
}

type compositionInput struct {
	Prompt string `json:"prompt"`
}

type compositionOutput struct {
	Local string `json:"local"`
	Model string `json:"model"`
}

func newCompositionDeployment(local, model agent.Deployment) (agent.Deployment, error) {
	if !local.Valid() || !model.Valid() {
		return agent.Deployment{}, agent.ErrInvalidDeployment
	}
	leafBudget := agent.Budget{
		Steps: agent.NewQuota(compositionChildBudgetSteps), Effects: agent.NewQuota(compositionChildBudgetEffects),
		Signals: agent.NewQuota(compositionChildBudgetSignals),
	}
	localBranch, err := newLocalBranch(local, leafBudget)
	if err != nil {
		return agent.Deployment{}, err
	}
	configuration, err := jsonv2.Marshal(leafBudget)
	if err != nil {
		return agent.Deployment{}, err
	}
	modelBranch, err := role.New(role.Config[compositionInput, string]{
		Name: "example.composition.model", Description: "Adapt the model child to the composition's input and text output.",
		Interaction: model, Budget: leafBudget,
		BuildInput: func(_ context.Context, input compositionInput) (interaction.Input, error) {
			return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(input.Prompt))}}, nil
		},
		DecodeOutput: func(_ context.Context, output interaction.Output) (string, error) {
			return chatclient.Text().Decode(output.ModelResponse)
		},
		ImplementationDigest: agent.ComputeDigest([]byte("example-composition-model-role")),
		ConfigurationDigest:  agent.ComputeDigest(configuration),
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	// Each branch budget includes the leaf's permanent allocation and the
	// adapter's own Steps, Effects, and Signals.
	branchBudget := agent.Budget{
		Steps: agent.NewQuota(compositionBranchBudgetSteps), Effects: agent.NewQuota(compositionBranchBudgetEffects),
		Signals: agent.NewQuota(compositionBranchBudgetSignals),
	}
	branches := []workflow.ForkBranch{
		compositionLocalIndex: {ID: "local", Deployment: localBranch, Budget: branchBudget},
		compositionModelIndex: {ID: "model", Deployment: modelBranch, Budget: branchBudget},
	}
	fork, err := workflow.Fork(workflow.ForkConfig[compositionInput, string, compositionOutput]{
		ID: "compose", WindowSize: uint32(len(branches)), Branches: branches,
		Reduce: func(_ context.Context, _ compositionInput, results []string) (compositionOutput, error) {
			return compositionOutput{Local: results[compositionLocalIndex], Model: results[compositionModelIndex]}, nil
		},
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	return bindCompositionWorkflow(compositionName, "Compose local and model children under one managed Workflow.", []workflow.Stage{fork})
}

func newLocalBranch(local agent.Deployment, budget agent.Budget) (agent.Deployment, error) {
	input, inputErr := workflow.Transform("build_input", func(_ context.Context, input compositionInput) (textInput, error) {
		return textInput{Text: input.Prompt}, nil
	})
	call, callErr := workflow.Call(workflow.CallConfig{ID: "run", Deployment: local, Budget: budget})
	output, outputErr := workflow.Transform("decode_output", func(_ context.Context, output textOutput) (string, error) {
		return output.Text, nil
	})
	if err := errors.Join(inputErr, callErr, outputErr); err != nil {
		return agent.Deployment{}, err
	}
	return bindCompositionWorkflow("example.composition.local", "Adapt a pure Definition to the composition's input and text output.", []workflow.Stage{input, call, output})
}

func bindCompositionWorkflow(name, description string, stages []workflow.Stage) (agent.Deployment, error) {
	definition, err := workflow.NewDefinition(workflow.DefinitionConfig{Name: name, Description: description, Stages: stages})
	if err != nil {
		return agent.Deployment{}, err
	}
	configuration, err := jsonv2.Marshal(definition.Topology(), jsonv2.Deterministic(true))
	if err != nil {
		return agent.Deployment{}, err
	}
	// The topology identifies the fixed wiring and grants; the implementation
	// label identifies the pure adapters in this checked example.
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte(name + "-implementation")),
		ConfigurationDigest:  agent.ComputeDigest(configuration),
	})
}

type compositionModel struct{}

func (compositionModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	if len(request.Messages) == 0 {
		return nil, errors.New("composition model requires one message")
	}
	message := chat.NewAssistantMessage(chat.NewTextPart("model: " + request.Messages[len(request.Messages)-1].Text()))
	return &chat.Response{Output: &chat.Output{
		Message: &message, FinishReason: chat.FinishReasonStop,
	}}, nil
}

func decodeCompleted[T any](result agent.Result) (T, error) {
	var zero T
	erased, ok := result.Termination().Output()
	if !ok {
		return zero, fmt.Errorf("process ended with %s", result.Termination().Status())
	}
	return erased.Decode[T]()
}

var _ agent.Definition = (*uppercaseDefinition)(nil)
