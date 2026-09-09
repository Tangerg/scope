// Command composition demonstrates that direct Engine embedding and a
// cross-Strategy composed Agent use the same Definition/Execution/Process
// contracts. It uses deterministic local components and requires no network.
package main

import (
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"

	agent "github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
)

const (
	compositionChildCount         = 2
	compositionChildBudgetSteps   = 20
	compositionChildBudgetEffects = 20
	compositionChildBudgetSignals = 40
)

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
	compositionDeployment, err := newCompositionDeployment(
		localDeployment.DeploymentRef(), modelDeployment.DeploymentRef(),
	)
	if err != nil {
		return err
	}
	resolver := deploymentResolver{
		localDeployment.DeploymentRef(): localDeployment,
		modelDeployment.DeploymentRef(): modelDeployment,
	}
	engine, err := agent.NewEngine(agent.EngineConfig{DeploymentResolver: resolver})
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
	inputSchema, err := agent.SchemaFor[textInput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	outputSchema, err := agent.SchemaFor[textOutput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "example.uppercase", Description: "Return input text in uppercase.",
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

func (u *uppercaseDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := u.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	decoded, err := input.Decode[textInput]()
	if err != nil {
		return nil, err
	}
	return &uppercaseExecution{Text: decoded.Text}, nil
}

func (*uppercaseDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !state.Valid() || state.Kind() != "example.uppercase" {
		return nil, agent.ErrInvalidExecutionState
	}
	var execution uppercaseExecution
	if err := jsonv2.Unmarshal(state.Payload(), &execution, jsonv2.RejectUnknownMembers(true)); err != nil {
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
	value, err := agent.EncodeOutput(textOutput{Text: strings.ToUpper(u.Text)})
	if err != nil {
		return agent.Transition{}, err
	}
	return agent.Complete(0, value)
}

func (u *uppercaseExecution) Snapshot() (agent.ExecutionState, error) {
	payload, err := json.Marshal(u)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("example.uppercase", payload)
}

func newModelDeployment() (agent.Deployment, error) {
	client, err := chatclient.New(compositionModel{}, chatclient.Config{})
	if err != nil {
		return agent.Deployment{}, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "example.composition_model", Description: "Return one deterministic composition response.",
		MaxModelCalls: 1,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Client: client})
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

type compositionDefinition struct {
	descriptor agent.Descriptor
	local      agent.DeploymentRef
	model      agent.DeploymentRef
}

func newCompositionDeployment(local, model agent.DeploymentRef) (agent.Deployment, error) {
	if !local.Valid() || !model.Valid() {
		return agent.Deployment{}, agent.ErrInvalidDeploymentRef
	}
	inputSchema, err := agent.SchemaFor[compositionInput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	outputSchema, err := agent.SchemaFor[compositionOutput]()
	if err != nil {
		return agent.Deployment{}, err
	}
	descriptor, err := agent.NewDescriptor(agent.DescriptorConfig{
		Name: "example.composition", Description: "Compose deterministic local and model child Processes.",
		InputSchema: inputSchema, OutputSchema: outputSchema,
	})
	if err != nil {
		return agent.Deployment{}, err
	}
	definition := &compositionDefinition{descriptor: descriptor, local: local, model: model}
	return agent.NewDeployment(agent.DeploymentConfig{
		Definition:           definition,
		ImplementationDigest: agent.ComputeDigest([]byte("example-composition-implementation")),
		ConfigurationDigest: agent.ComputeDigest([]byte(
			"example-composition:" + local.Digest().String() + ":" + model.Digest().String(),
		)),
	})
}

func (c *compositionDefinition) Descriptor() agent.Descriptor { return c.descriptor }

func (c *compositionDefinition) Start(input agent.Input) (agent.Execution, error) {
	if err := c.descriptor.ValidateInput(input); err != nil {
		return nil, err
	}
	decoded, err := input.Decode[compositionInput]()
	if err != nil {
		return nil, err
	}
	return &compositionExecution{
		local: c.local, model: c.model,
		state: compositionState{Phase: compositionReady, Prompt: decoded.Prompt},
	}, nil
}

func (c *compositionDefinition) Restore(state agent.ExecutionState) (agent.Execution, error) {
	if !state.Valid() || state.Kind() != "example.composition" {
		return nil, agent.ErrInvalidExecutionState
	}
	var decoded compositionState
	if err := jsonv2.Unmarshal(state.Payload(), &decoded, jsonv2.RejectUnknownMembers(true)); err != nil {
		return nil, err
	}
	if err := decoded.validate(); err != nil {
		return nil, err
	}
	return &compositionExecution{local: c.local, model: c.model, state: decoded}, nil
}

type compositionPhase string

const (
	compositionReady                 compositionPhase = "ready"
	compositionAwaitingChildStarts   compositionPhase = "awaiting_child_starts"
	compositionAwaitingChildWaitOpen compositionPhase = "awaiting_child_wait_open"
	compositionWaitingChildren       compositionPhase = "waiting_children"
	compositionCompleted             compositionPhase = "completed"
)

type compositionState struct {
	Phase    compositionPhase  `json:"phase"`
	Prompt   string            `json:"prompt"`
	ChildIDs []agent.ProcessID `json:"child_ids,omitempty"`
	WaitID   *agent.WaitID     `json:"wait_id,omitempty"`
}

func (c compositionState) validate() error {
	switch c.Phase {
	case compositionReady, compositionAwaitingChildStarts, compositionCompleted:
		if len(c.ChildIDs) != 0 || c.WaitID != nil {
			return agent.ErrInvalidExecutionState
		}
	case compositionAwaitingChildWaitOpen, compositionWaitingChildren:
		if len(c.ChildIDs) != compositionChildCount || !c.ChildIDs[0].Valid() || !c.ChildIDs[1].Valid() || c.ChildIDs[0] == c.ChildIDs[1] {
			return agent.ErrInvalidExecutionState
		}
		if c.Phase == compositionAwaitingChildWaitOpen && c.WaitID != nil {
			return agent.ErrInvalidExecutionState
		}
		if c.Phase == compositionWaitingChildren && (c.WaitID == nil || !c.WaitID.Valid()) {
			return agent.ErrInvalidExecutionState
		}
	default:
		return agent.ErrInvalidExecutionState
	}
	return nil
}

type compositionExecution struct {
	local agent.DeploymentRef
	model agent.DeploymentRef
	state compositionState
}

func (c *compositionExecution) Step(
	_ context.Context,
	signals []agent.Signal,
) (agent.Transition, error) {
	switch c.state.Phase {
	case compositionReady:
		if len(signals) != 0 {
			return agent.Transition{}, agent.ErrInvalidSignal
		}
		return c.startChildren()
	case compositionAwaitingChildStarts:
		return c.waitForChildren(signals)
	case compositionAwaitingChildWaitOpen:
		if len(signals) == 0 {
			return agent.Transition{}, errors.New("composition wait was not opened")
		}
		opened, err := agent.ParseChildWaitOpened(signals[0])
		if err != nil {
			return agent.Transition{}, err
		}
		spec := opened.Spec()
		if spec.Key.String() != "composition" || !slices.Equal(spec.Children, c.state.ChildIDs) || spec.Condition != agent.AllChildren() {
			return agent.Transition{}, agent.ErrInvalidChildWait
		}
		waitID := opened.WaitID()
		c.state.WaitID = &waitID
		c.state.Phase = compositionWaitingChildren
		return agent.Wait(1, opened.WaitID())
	case compositionWaitingChildren:
		return c.complete(signals)
	default:
		return agent.Transition{}, errors.New("composition execution cannot advance")
	}
}

func (c *compositionExecution) startChildren() (agent.Transition, error) {
	localInput, err := agent.EncodeInput(textInput{Text: c.state.Prompt})
	if err != nil {
		return agent.Transition{}, err
	}
	modelInput, err := agent.EncodeInput(interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart(c.state.Prompt)),
	}})
	if err != nil {
		return agent.Transition{}, err
	}
	localKey, err := agent.ParseChildKey("local")
	if err != nil {
		return agent.Transition{}, err
	}
	modelKey, err := agent.ParseChildKey("model")
	if err != nil {
		return agent.Transition{}, err
	}
	budget, err := agent.NewBudget(agent.BudgetConfig{
		Steps: compositionChildBudgetSteps, Effects: compositionChildBudgetEffects,
		Signals: compositionChildBudgetSignals,
	})
	if err != nil {
		return agent.Transition{}, err
	}
	localEffect, err := agent.StartChild(agent.ChildSpec{
		Key: localKey, DeploymentRef: c.local, Input: localInput, Budget: budget,
	})
	if err != nil {
		return agent.Transition{}, err
	}
	modelEffect, err := agent.StartChild(agent.ChildSpec{
		Key: modelKey, DeploymentRef: c.model, Input: modelInput, Budget: budget,
	})
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.Phase = compositionAwaitingChildStarts
	return agent.Continue(0, localEffect, modelEffect)
}

func (c *compositionExecution) waitForChildren(signals []agent.Signal) (agent.Transition, error) {
	if len(signals) != compositionChildCount {
		return agent.Transition{}, errors.New("composition requires two child-start results")
	}
	children := make([]agent.ProcessID, compositionChildCount)
	keys := [...]string{"local", "model"}
	references := [...]agent.DeploymentRef{c.local, c.model}
	var failure *agent.Failure
	for index, signal := range signals {
		started, err := agent.ParseChildStartResult(signal)
		if err != nil {
			return agent.Transition{}, err
		}
		if started.Key().String() != keys[index] || started.DeploymentRef() != references[index] {
			return agent.Transition{}, agent.ErrInvalidChildStart
		}
		if startFailure, failed := started.Failure(); failed {
			if failure == nil {
				failure = &startFailure
			}
			continue
		}
		childID, present := started.ProcessID()
		if !present {
			return agent.Transition{}, agent.ErrInvalidChildStart
		}
		children[index] = childID
	}
	if failure != nil {
		return agent.Fail(compositionChildCount, *failure)
	}
	waitKey, err := agent.ParseWaitKey("composition")
	if err != nil {
		return agent.Transition{}, err
	}
	waitEffect, err := agent.WaitForChildren(agent.ChildWaitSpec{
		Key: waitKey, Children: children, Condition: agent.AllChildren(),
	})
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.ChildIDs = children
	c.state.Phase = compositionAwaitingChildWaitOpen
	return agent.Continue(compositionChildCount, waitEffect)
}

func (c *compositionExecution) complete(
	signals []agent.Signal,
) (agent.Transition, error) {
	if len(signals) == 0 {
		return agent.Transition{}, errors.New("composition child results are missing")
	}
	completed, err := agent.ParseChildrenCompleted(signals[0])
	if err != nil {
		return agent.Transition{}, err
	}
	if c.state.WaitID == nil || completed.WaitID() != *c.state.WaitID || completed.Key().String() != "composition" {
		return agent.Transition{}, errors.New("composition received another wait's result")
	}
	outcomes := completed.Outcomes()
	if len(outcomes) != compositionChildCount {
		return agent.Transition{}, agent.ErrInvalidChildWait
	}
	keys := [...]string{"local", "model"}
	for index, outcome := range outcomes {
		if outcome.Key().String() != keys[index] || outcome.Result().ProcessID() != c.state.ChildIDs[index] {
			return agent.Transition{}, agent.ErrInvalidChildWait
		}
	}
	var output compositionOutput
	for _, outcome := range outcomes {
		result := outcome.Result()
		if result.Status() != agent.StatusCompleted {
			failure, failureErr := agent.NewFailure(
				agent.FailureKindExecution, "example.child.failed", "a composition child did not complete",
			)
			if failureErr != nil {
				return agent.Transition{}, failureErr
			}
			return agent.Fail(1, failure)
		}
		erased, present := result.Output()
		if !present {
			return agent.Transition{}, agent.ErrInvalidChildWait
		}
		switch outcome.Key().String() {
		case "local":
			decoded, decodeErr := erased.Decode[textOutput]()
			if decodeErr != nil {
				return agent.Transition{}, decodeErr
			}
			output.Local = decoded.Text
		case "model":
			decoded, decodeErr := erased.Decode[interaction.Output]()
			if decodeErr != nil {
				return agent.Transition{}, decodeErr
			}
			output.Model = decoded.ModelResponse.Text()
		}
	}
	erased, err := agent.EncodeOutput(output)
	if err != nil {
		return agent.Transition{}, err
	}
	c.state.Phase = compositionCompleted
	c.state.ChildIDs = nil
	c.state.WaitID = nil
	return agent.Complete(1, erased)
}

func (c *compositionExecution) Snapshot() (agent.ExecutionState, error) {
	if err := c.state.validate(); err != nil {
		return agent.ExecutionState{}, err
	}
	payload, err := json.Marshal(c.state)
	if err != nil {
		return agent.ExecutionState{}, err
	}
	return agent.NewExecutionState("example.composition", payload)
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

type deploymentResolver map[agent.DeploymentRef]agent.Deployment

func (d deploymentResolver) Resolve(
	reference agent.DeploymentRef,
) (agent.Deployment, error) {
	deployment, found := d[reference]
	if !found {
		return agent.Deployment{}, errors.New("exact Deployment is unavailable")
	}
	return deployment, nil
}

func decodeCompleted[T any](result agent.Result) (T, error) {
	var zero T
	erased, ok := result.Output()
	if !ok {
		return zero, fmt.Errorf("process ended with %s", result.Status())
	}
	return erased.Decode[T]()
}
