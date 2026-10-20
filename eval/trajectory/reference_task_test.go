package trajectory_test

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/jsonschema"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/eval"
	"github.com/Tangerg/scope/eval/trajectory"
)

type referenceInput struct {
	Note         string `json:"note"`
	Approved     bool   `json:"approved"`
	RepeatInput  bool   `json:"repeat_input"`
	LoseResponse bool   `json:"lose_response"`
	Reconcile    bool   `json:"reconcile"`
	WrongAnswer  bool   `json:"wrong_answer"`
	Expensive    bool   `json:"expensive"`
	MissingUsage bool   `json:"missing_usage"`
}

type referenceNote struct {
	Text string `json:"text"`
}

type referenceAnswer struct {
	Approved bool `json:"approved"`
}

type referenceRow struct {
	EffectID agent.EffectID `json:"effect_id"`
	Note     referenceNote  `json:"note"`
}

// The isolated target owns business writes. Agent snapshots and observations
// cannot manufacture a receipt, and a repeated identity cannot change its note.
type referenceService struct {
	mu   sync.Mutex
	rows map[agent.EffectID]referenceNote
}

func (r *referenceService) write(id agent.EffectID, note referenceNote) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if prior, exists := r.rows[id]; exists && prior != note {
		return "", errors.New("reference: conflicting business write")
	}
	r.rows[id] = note
	return "stored:" + note.Text, nil
}

func (r *referenceService) lookup(id agent.EffectID) (chat.ToolOutput, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	note, exists := r.rows[id]
	if !exists {
		return chat.ToolOutput{}, errors.New("reference: no authoritative business receipt")
	}
	return chat.NewTextToolOutput("stored:" + note.Text), nil
}

func (r *referenceService) snapshot() []referenceRow {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := make([]referenceRow, 0, len(r.rows))
	for id, note := range r.rows {
		rows = append(rows, referenceRow{EffectID: id, Note: note})
	}
	slices.SortFunc(rows, func(a, b referenceRow) int {
		return strings.Compare(a.EffectID.String(), b.EffectID.String())
	})
	return rows
}

// Coverage comes from Engine dispatch facts and these two exact bindings,
// independently of whether semantic observers publish a response.
type referenceRecorder struct {
	trajectory.Recorder
	modelDeployment agent.DeploymentRef
	toolDeployment  agent.DeploymentRef
	mu              sync.Mutex
	coverage        trajectory.Coverage
}

func (r *referenceRecorder) OnEvent(ctx context.Context, event agent.Event) {
	if fact, ok := event.EffectStarted(); ok && fact.Target() == agent.EffectTargetDispatcher {
		id, _ := event.EffectID()
		incarnation, _ := event.TreeIncarnationID()
		reference := trajectory.EffectReference{
			ProcessID: event.Relation().ProcessID(), TreeIncarnationID: incarnation, EffectID: id,
		}
		r.mu.Lock()
		switch event.DeploymentRef() {
		case r.modelDeployment:
			if !slices.Contains(r.coverage.Models, reference) {
				r.coverage.Models = append(r.coverage.Models, reference)
			}
		case r.toolDeployment:
			if !slices.Contains(r.coverage.Tools, reference) {
				r.coverage.Tools = append(r.coverage.Tools, reference)
			}
		}
		r.mu.Unlock()
	}
	r.Recorder.OnEvent(ctx, event)
}

func (r *referenceRecorder) classified() trajectory.Coverage {
	r.mu.Lock()
	defer r.mu.Unlock()
	return trajectory.Coverage{Models: slices.Clone(r.coverage.Models), Tools: slices.Clone(r.coverage.Tools)}
}

type referenceEvidence struct {
	Trajectory        trajectory.Trajectory `json:"trajectory"`
	Head              agent.TreeSnapshot    `json:"head"`
	Input             agent.SignalRequest   `json:"input"`
	Rows              []referenceRow        `json:"rows"`
	WritesBeforeInput int                   `json:"writes_before_input"`
}

func referenceModel(input referenceInput) chat.Model {
	return chat.ModelFunc(func(ctx context.Context, request *chat.Request) (*chat.Response, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := request.Validate(); err != nil {
			return nil, err
		}
		// Scripted accounting exercises the cost gate, not provider pricing or
		// a tokenizer. Only the model response originates these usage facts.
		responseMetadata := &chat.ResponseMetadata{}
		if !input.MissingUsage {
			responseMetadata.Usage = &chat.Usage{InputTokens: 8, OutputTokens: 2}
			if input.Expensive {
				responseMetadata.Usage.InputTokens = 28
			}
		}
		for _, message := range request.Messages {
			for _, part := range message.Parts {
				if part.Kind != chat.PartToolResult {
					continue
				}
				text, ok := part.ToolResult.Output.Text()
				if !ok {
					return nil, errors.New("reference: expected a text receipt")
				}
				if input.WrongAnswer {
					text = "incorrect receipt"
				}
				return &chat.Response{Metadata: responseMetadata, Output: &chat.Output{
					Message: new(chat.NewAssistantMessage(chat.NewTextPart(text))), FinishReason: chat.FinishReasonStop,
				}}, nil
			}
		}
		arguments, err := jsonv2.Marshal(referenceNote{Text: input.Note})
		if err != nil {
			return nil, err
		}
		return &chat.Response{Metadata: responseMetadata, Output: &chat.Output{
			Message: new(chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
				ID: "call:record", Name: "record_note", Arguments: string(arguments),
			}))), FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	})
}

func referenceTools(input referenceInput, service *referenceService, recorder *referenceRecorder) (interaction.ToolSet, error) {
	answerSchema, err := jsonschema.For[referenceAnswer]()
	if err != nil {
		return interaction.ToolSet{}, err
	}
	executable, err := newContinuingTool(tool.FuncConfig{Name: "record_note", Description: "Record a note after explicit input."},
		func(ctx context.Context, callInput tool.Invocation) (tool.Result, error) {
			var note referenceNote
			if decodeErr := jsonv2.Unmarshal(callInput.Arguments(), &note, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
				return tool.Result{}, decodeErr
			}
			continuation, resumed := callInput.Continuation()
			if !resumed {
				prompt, promptErr := jsonv2.Marshal(note)
				if promptErr != nil {
					return tool.Result{}, promptErr
				}
				request, requestErr := tool.NewInputRequest(tool.InputRequestConfig{Prompt: prompt, ResponseSchema: answerSchema, ContinuationState: []byte(`null`)})
				if requestErr != nil {
					return tool.Result{}, requestErr
				}
				return tool.Result{InputRequest: &request}, nil
			}
			var answer referenceAnswer
			if decodeErr := jsonv2.Unmarshal(continuation.Response(), &answer, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
				return tool.Result{}, decodeErr
			}
			if !answer.Approved {
				failure, rejectionErr := tool.NewFailure(tool.FailureConfig{
					Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("rejected"),
				})
				if rejectionErr != nil {
					return tool.Result{}, rejectionErr
				}
				return tool.Result{}, failure
			}
			invocation, ok := interaction.ToolInvocationFromContext(ctx)
			if !ok {
				return tool.Result{}, errors.New("reference: missing Engine attribution")
			}
			text, writeErr := service.write(invocation.EffectID(), note)
			if writeErr != nil {
				return tool.Result{}, writeErr
			}
			if input.LoseResponse {
				return tool.Result{}, errors.New("reference: response lost after acknowledged business write")
			}
			return tool.Result{Output: chat.NewTextToolOutput(text)}, nil
		})
	if err != nil {
		return interaction.ToolSet{}, err
	}
	return interaction.NewToolSet(interaction.ToolSetConfig{
		Name: "reference.tools", Description: "Isolated reference write binding.", Tools: []tool.Tool{executable}, Observer: recorder,
		ImplementationDigest: agent.ComputeDigest([]byte("reference-write")),
		ConfigurationDigest:  agent.ComputeDigest([]byte(fmt.Sprintf("lose_response=%t", input.LoseResponse))),
	})
}

func runReferenceTask(ctx context.Context, input referenceInput) (execution eval.Execution[referenceEvidence], runErr error) {
	service := &referenceService{rows: make(map[agent.EffectID]referenceNote)}
	recorder := &referenceRecorder{}
	tools, err := referenceTools(input, service, recorder)
	if err != nil {
		return execution, err
	}
	definition, err := interaction.NewDefinition(interaction.DefinitionConfig{
		Name: "reference.record", Description: "Request input, write a note, and report its receipt.",
		MaxModelCalls: agent.NewQuota(2), Tools: tools,
	})
	if err != nil {
		return execution, err
	}
	dispatcher, err := interaction.NewDispatcher(definition, interaction.DispatcherConfig{Model: referenceModel(input), Observer: recorder})
	if err != nil {
		return execution, err
	}
	configuration, err := jsonv2.Marshal(input, jsonv2.Deterministic(true))
	if err != nil {
		return execution, err
	}
	deployment, err := agent.NewDeployment(agent.DeploymentConfig{
		Definition: definition, Dispatcher: dispatcher,
		ImplementationDigest: agent.ComputeDigest([]byte("reference-interaction")), ConfigurationDigest: agent.ComputeDigest(configuration),
	})
	if err != nil {
		return execution, err
	}
	recorder.modelDeployment, recorder.toolDeployment = deployment.DeploymentRef(), tools.Deployment().DeploymentRef()
	store := agent.NewMemoryTreeCommitter()
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{recorder, observations}})
	if err != nil {
		return execution, err
	}
	defer func() {
		closeContext, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		runErr = errors.Join(runErr, engine.Close(closeContext))
	}()
	payload, err := agent.EncodePayload(interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("Record this note after asking for input: " + input.Note)),
	}})
	if err != nil {
		return execution, err
	}
	root, err := engine.Start(ctx, deployment, payload)
	if err != nil {
		return execution, err
	}
	if _, waitErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.StepCommitted()
		return ok && fact.Status() == agent.StatusWaiting && event.DeploymentRef() == tools.Deployment().DeploymentRef()
	}); waitErr != nil {
		return execution, waitErr
	}
	waitingHead, err := engine.CaptureTree(ctx, root.Relation().ProcessID())
	if err != nil {
		return execution, err
	}
	pending, err := interaction.PendingToolInputs(waitingHead)
	if err != nil || len(pending) != 1 {
		return execution, errors.Join(errors.New("reference: expected exactly one input wait"), err)
	}
	child, ok := engine.Process(pending[0].ProcessID())
	if !ok {
		return execution, errors.New("reference: missing input child")
	}
	writesBeforeInput := len(service.snapshot())
	answer, err := jsonv2.Marshal(referenceAnswer{Approved: input.Approved})
	if err != nil {
		return execution, err
	}
	id, err := agent.ParseSignalID("signal:reference-input")
	if err != nil {
		return execution, err
	}
	signal, err := pending[0].ResponseSignal(id, answer)
	if err != nil {
		return execution, err
	}
	// Pausing holds the same wait across delivery acknowledgments; no scheduling
	// race can turn the duplicate-delivery case into a terminal-Process test.
	if input.RepeatInput {
		if pauseErr := child.Pause(ctx, "verify repeated delivery"); pauseErr != nil {
			return execution, pauseErr
		}
	}
	if accepted, deliveryErr := child.DeliverSignals(ctx, signal); deliveryErr != nil || !accepted {
		return execution, errors.Join(errors.New("reference: input was not admitted"), deliveryErr)
	}
	if input.RepeatInput {
		if accepted, repeatErr := child.DeliverSignals(ctx, signal); repeatErr != nil || accepted {
			return execution, errors.Join(errors.New("reference: duplicate input changed admission"), repeatErr)
		}
		if resumeErr := child.Resume(ctx); resumeErr != nil {
			return execution, resumeErr
		}
	}
	if input.LoseResponse && input.Approved {
		event, observationErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
			fact, ok := event.EffectFinished()
			return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown && event.Relation().ProcessID() == child.Relation().ProcessID()
		})
		if observationErr != nil {
			return execution, observationErr
		}
		if input.Reconcile {
			recoveryHead, captureErr := engine.CaptureTree(ctx, root.Relation().ProcessID())
			if captureErr != nil {
				return execution, captureErr
			}
			effectID, _ := event.EffectID()
			request, ok := recoveryHead.EffectRequest(child.Relation().ProcessID(), effectID)
			if !ok {
				return execution, errors.New("reference: missing original write request")
			}
			receipt, lookupErr := service.lookup(effectID)
			if lookupErr != nil {
				return execution, lookupErr
			}
			settlement, settlementErr := tools.SettleToolResult(request, receipt, interaction.ResultSucceeded, nil)
			if settlementErr != nil {
				return execution, settlementErr
			}
			if resolutionErr := child.ResolveUnknownEffect(ctx, effectID, settlement); resolutionErr != nil {
				return execution, resolutionErr
			}
		} else if killErr := root.Kill(ctx, "business outcome remains unresolved"); killErr != nil {
			return execution, killErr
		}
	}
	result, err := root.Await(ctx)
	if err != nil {
		return execution, err
	}
	coverage := recorder.classified()
	recorded, err := recorder.Take(ctx, root, &coverage)
	if err != nil {
		return execution, err
	}
	head, found, err := store.LoadTree(ctx, root.Relation().ProcessID())
	if err != nil || !found {
		return execution, errors.Join(errors.New("reference: no acknowledged tree head"), err)
	}
	execution = eval.Execution[referenceEvidence]{Status: eval.ExecutionCompleted, Output: &referenceEvidence{
		Trajectory: recorded, Head: head, Input: signal, Rows: service.snapshot(), WritesBeforeInput: writesBeforeInput,
	}}
	switch status := result.Termination().Status(); status {
	case agent.StatusCompleted:
	case agent.StatusKilled, agent.StatusCanceled, agent.StatusTimedOut:
		execution.Status, execution.Reason = eval.ExecutionCanceled, "reference: stopped with "+status.String()
	default:
		execution.Status, execution.Reason = eval.ExecutionFailed, "reference: stopped with "+status.String()
	}
	return execution, nil
}

// Example_referenceTask uses managed execution because the Tool must suspend
// for input and an acknowledged write may lose its response. It is a protocol
// fixture, not evidence of live-model quality or durable database transactions.
func Example_referenceTask() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	execution, err := runReferenceTask(ctx, referenceInput{Note: "release note", Approved: true, LoseResponse: true, Reconcile: true})
	if err != nil {
		panic(err)
	}
	evidence := execution.Output
	fmt.Println(execution.Status, len(evidence.Rows), evidence.WritesBeforeInput)
	fmt.Println(evidence.Trajectory.ToolCalls()[1].Outcome())
	// Output:
	// completed 1 0
	// unknown
}

type referenceNoteTool struct {
	definition chat.ToolDefinition
	call       func(context.Context, tool.Invocation) (tool.Result, error)
}

func newContinuingTool(config tool.FuncConfig, call func(context.Context, tool.Invocation) (tool.Result, error)) (referenceNoteTool, error) {
	schema, err := jsonschema.For[referenceNote]()
	if err != nil {
		return referenceNoteTool{}, err
	}
	return referenceNoteTool{definition: chat.ToolDefinition{Name: config.Name, Description: config.Description, InputSchema: schema.JSON()}, call: call}, nil
}
func (r referenceNoteTool) Definition() chat.ToolDefinition { return r.definition.Clone() }
func (r referenceNoteTool) Call(ctx context.Context, input tool.Invocation) (tool.Result, error) {
	return r.call(ctx, input)
}
