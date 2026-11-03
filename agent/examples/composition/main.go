// Command composition demonstrates that direct Runtime embedding and a
// cross-Strategy Agent composed from Workflow and Interaction atoms use the same
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

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/agent/workflow"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/jsonschema"
)

const (
	compositionChildBudgetSteps     = 20
	compositionChildBudgetEffects   = 20
	compositionChildBudgetMessages  = 40
	compositionBranchBudgetSteps    = 40
	compositionBranchBudgetEffects  = 40
	compositionBranchBudgetMessages = 80
	compositionMaxModelCalls        = 1
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
	runtime, err := agentrun.New(ctx, agentrun.Config{Build: "example-composition", Store: agentrun.NewMemoryStore()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close(context.WithoutCancel(ctx))) }()

	localProgram, err := newUppercaseProgram(runtime.Compiler())
	if err != nil {
		return err
	}
	modelProgram, err := newModelProgram(runtime.Compiler())
	if err != nil {
		return err
	}
	compositionProgram, err := newCompositionProgram(runtime.Compiler(), localProgram, modelProgram)
	if err != nil {
		return err
	}

	embeddedHandle, err := runtime.Start(ctx, localProgram, agentrun.StartRequest[textInput]{ID: agentrun.StartID{Scope: "example", Key: "embedded"}, Input: textInput{Text: "embedded"}})
	if err != nil {
		return err
	}
	embeddedResult, err := embeddedHandle.Await(ctx)
	if err != nil {
		return err
	}
	embedded, err := embeddedResult.Output()
	if err != nil {
		return err
	}

	compositionHandle, err := runtime.Start(ctx, compositionProgram, agentrun.StartRequest[compositionInput]{ID: agentrun.StartID{Scope: "example", Key: "composition"}, Input: compositionInput{Prompt: "composition"}})
	if err != nil {
		return err
	}
	compositionResult, err := compositionHandle.Await(ctx)
	if err != nil {
		return err
	}
	composed, err := compositionResult.Output()
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

type uppercaseDefinition struct{ descriptor agentrun.Descriptor }

func newUppercaseProgram(compiler *agentrun.Compiler) (agentrun.Program[textInput, textOutput], error) {
	inputSchema, err := jsonschema.For[textInput]()
	if err != nil {
		return agentrun.Program[textInput, textOutput]{}, err
	}
	outputSchema, err := jsonschema.For[textOutput]()
	if err != nil {
		return agentrun.Program[textInput, textOutput]{}, err
	}
	descriptor, err := agentrun.NewDescriptor(agentrun.DescriptorConfig{
		Name: uppercaseStateKind, Description: "Return input text in uppercase.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return agentrun.Program[textInput, textOutput]{}, err
	}
	return agentrun.Bind[textInput, textOutput](compiler, agentrun.BindingConfig{
		Definition: &uppercaseDefinition{descriptor: descriptor},
		Revision:   "uppercase-v1", Configuration: struct{}{},
	})
}

func (u *uppercaseDefinition) Descriptor() agentrun.Descriptor { return u.descriptor }

func (*uppercaseDefinition) ChildPrograms() []agentrun.Program[agentrun.Payload, agentrun.Payload] {
	return nil
}

func (u *uppercaseDefinition) Start(input agentrun.Payload) (agentrun.Execution, error) {
	if err := u.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	decoded, err := input.Decode[textInput]()
	if err != nil {
		return nil, err
	}
	return &uppercaseExecution{Text: decoded.Text}, nil
}

func (*uppercaseDefinition) Restore(ctx context.Context, state agentrun.ExecutionState) (agentrun.Execution, error) {
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

func (u *uppercaseExecution) Step(_ context.Context, turn agentrun.Turn) (agentrun.Transition, error) {
	if len(turn.Messages) != 0 {
		return agentrun.Transition{}, agentrun.ErrInvalidMessage
	}
	if u.Done {
		return agentrun.Transition{}, errors.New("uppercase execution already completed")
	}
	u.Done = true
	value, err := agentrun.EncodePayload(textOutput{Text: strings.ToUpper(u.Text)})
	if err != nil {
		return agentrun.Transition{}, err
	}
	return agentrun.Complete(0, value)
}

func (u *uppercaseExecution) Snapshot() (agentrun.ExecutionState, error) {
	payload, err := jsonv2.Marshal(u)
	if err != nil {
		return agentrun.ExecutionState{}, err
	}
	return agentrun.ParseExecutionState(uppercaseStateKind, payload)
}

func newModelProgram(compiler *agentrun.Compiler) (agentrun.Program[interaction.Input, interaction.Output], error) {
	client, err := chatclient.New(compositionModel{})
	if err != nil {
		return agentrun.Program[interaction.Input, interaction.Output]{}, err
	}
	return interaction.Compile(compiler, interaction.Config{Name: "example.composition_model", Description: "Return one deterministic composition response.", MaxModelCalls: agentrun.NewQuota(compositionMaxModelCalls), Model: client, Revision: "composition-model-v1"})
}

type compositionInput struct {
	Prompt string `json:"prompt"`
}

type compositionOutput struct {
	Local string `json:"local"`
	Model string `json:"model"`
}

func newCompositionProgram(compiler *agentrun.Compiler, local agentrun.Program[textInput, textOutput], model agentrun.Program[interaction.Input, interaction.Output]) (agentrun.Program[compositionInput, compositionOutput], error) {
	var zero agentrun.Program[compositionInput, compositionOutput]
	if !local.Valid() || !model.Valid() {
		return zero, agentrun.ErrInvalidProgram
	}
	leafBudget := agentrun.Budget{
		Steps: agentrun.NewQuota(compositionChildBudgetSteps), Effects: agentrun.NewQuota(compositionChildBudgetEffects),
		Messages: agentrun.NewQuota(compositionChildBudgetMessages),
	}
	localBranch, err := workflow.Adapt(compiler, workflow.AdaptConfig{
		Name: "example.composition.local", Description: "Adapt a pure Definition to the composition's input and text output.",
		Revision: "composition-local-branch-v1", Budget: leafBudget,
	}, local, func(_ context.Context, input compositionInput) (textInput, error) {
		return textInput{Text: input.Prompt}, nil
	}, func(_ context.Context, output textOutput) (string, error) {
		return output.Text, nil
	})
	if err != nil {
		return zero, err
	}
	modelBranch, err := workflow.Adapt(compiler, workflow.AdaptConfig{
		Name: "example.composition.model", Description: "Render model input, invoke it, and decode its output.",
		Revision: "composition-model-branch-v1", Budget: leafBudget,
	}, model, func(_ context.Context, input compositionInput) (interaction.Input, error) {
		return interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart(input.Prompt))}}, nil
	}, func(_ context.Context, output interaction.Output) (string, error) {
		return chatclient.Text().Decode(output.ModelResponse)
	})
	if err != nil {
		return zero, err
	}
	branchBudget := agentrun.Budget{
		Steps: agentrun.NewQuota(compositionBranchBudgetSteps), Effects: agentrun.NewQuota(compositionBranchBudgetEffects),
		Messages: agentrun.NewQuota(compositionBranchBudgetMessages),
	}
	branches := []workflow.ForkBranch{
		compositionLocalIndex: {ID: "local", Child: agentrun.ChildBinding{Program: localBranch.Erase(), Budget: branchBudget}},
		compositionModelIndex: {ID: "model", Child: agentrun.ChildBinding{Program: modelBranch.Erase(), Budget: branchBudget}},
	}
	fork, err := workflow.Fork(workflow.ForkConfig[compositionInput, string, compositionOutput]{
		ID: "compose", WindowSize: uint32(len(branches)), Branches: branches,
		Reduce: func(_ context.Context, _ compositionInput, results []string) (compositionOutput, error) {
			return compositionOutput{Local: results[compositionLocalIndex], Model: results[compositionModelIndex]}, nil
		},
	})
	if err != nil {
		return zero, err
	}
	return workflow.Compile[compositionInput, compositionOutput](compiler, workflow.Config{
		Name: compositionName, Description: "Compose local and model children under one managed Workflow.",
		Revision: "composition-v1", Stages: []workflow.Stage{fork},
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

var _ agentrun.Definition = (*uppercaseDefinition)(nil)
