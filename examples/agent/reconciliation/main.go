package main

import (
	"context"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := run(ctx, os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) error {
	for _, scenario := range []struct {
		name         string
		reject       bool
		availability queryAvailability
	}{
		{name: "confirmed"},
		{name: "rejected", reject: true},
		{name: "missing", availability: queryMissing},
		{name: "unavailable", availability: queryUnavailable},
	} {
		remote := &remoteLedger{reject: scenario.reject, availability: scenario.availability}
		result, err := runScenario(ctx, remote)
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		unknown := 0
		for _, snapshot := range result.Head.ProcessSnapshots() {
			unknown += len(snapshot.UnknownEffectIDs())
		}
		if _, writeErr := fmt.Fprintf(output, "%s: %s unknown=%d writes=%d attempts=%d model_calls=%d result=%q reason=%q\n",
			scenario.name, result.Result.Termination().Status(), unknown, result.Remote.Writes, result.Remote.Attempts,
			result.ModelCalls, result.Message, result.Reason); writeErr != nil {
			return writeErr
		}
	}
	return nil
}

type scenarioResult struct {
	Before     agent.TreeSnapshot
	Head       agent.TreeSnapshot
	Request    agent.EffectRequest
	Result     agent.Result
	Remote     ledgerReport
	ModelCalls int32
	Message    string
	Reason     string
}

func runScenario(ctx context.Context, remote *remoteLedger) (result scenarioResult, runErr error) {
	model := &receiptModel{}
	deployment, tools, err := newDeployment(remote, model)
	if err != nil {
		return result, err
	}
	store := agent.NewMemoryTreeCommitter()
	observations := &agenttest.ObservationRecorder{}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store, EventListeners: []agent.EventListener{observations}})
	if err != nil {
		return result, err
	}
	var root *agent.Process
	defer func() { runErr = errors.Join(runErr, closeTree(ctx, engine, root)) }()
	input, err := deployment.Descriptor().EncodeInput(interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("Record one release note and report its receipt.")),
	}})
	if err != nil {
		return result, err
	}
	root, err = engine.Start(ctx, deployment, input)
	if err != nil {
		return result, err
	}
	if _, waitErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		fact, ok := event.EffectFinished()
		return ok && fact.SettlementStatus() == agent.SettlementStatusUnknown
	}); waitErr != nil {
		return result, waitErr
	}
	// CaptureTree releases its barrier when it returns. Pause the live parent
	// first so it cannot advance the head between the load and writer activation.
	if pauseErr := root.Pause(ctx, "hand off the uncertain operation"); pauseErr != nil {
		return result, pauseErr
	}
	if _, waitErr := observations.AwaitEvent(ctx, func(event agent.Event) bool {
		return event.Name() == agent.EventProcessPaused && event.Relation().ProcessID() == root.Relation().ProcessID()
	}); waitErr != nil {
		return result, waitErr
	}
	// After an actual runtime failure, read the store directly: CaptureTree
	// cannot recover private state.
	if _, captureErr := engine.CaptureTree(ctx, root.Relation().ProcessID()); captureErr != nil {
		return result, captureErr
	}
	head, found, err := store.LoadTree(ctx, root.Relation().ProcessID())
	if err != nil || !found {
		return result, errors.Join(errors.New("example: no authoritative head"), err)
	}
	request, err := unknownRequest(head)
	if err != nil {
		return result, err
	}
	replacement, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: store})
	if err != nil {
		return result, err
	}
	var restored *agent.Process
	defer func() { runErr = errors.Join(runErr, closeTree(ctx, replacement, restored)) }()
	restored, err = replacement.RestoreTree(ctx, deployment, head)
	if err != nil {
		return result, err
	}
	owner, found := replacement.Process(request.Relation().ProcessID())
	if !found {
		return result, errors.New("example: missing restored Tool Process")
	}
	if resumeErr := restored.Resume(ctx); resumeErr != nil {
		return result, resumeErr
	}
	resolved, reason, err := reconcile(ctx, remote.lookup, tools, request, owner)
	if err != nil {
		return result, err
	}
	if !resolved {
		if cancelErr := restored.RequestCancellation(ctx, reason); cancelErr != nil {
			return result, cancelErr
		}
	}
	completed, err := restored.Await(ctx)
	if err != nil {
		return result, err
	}
	if joinErr := restored.Join(ctx); joinErr != nil {
		return result, joinErr
	}
	final, found, err := store.LoadTree(ctx, head.RootID())
	if err != nil || !found {
		return result, errors.Join(errors.New("example: missing final head"), err)
	}
	result = scenarioResult{Before: head, Head: final, Request: request, Result: completed, Remote: remote.report(), ModelCalls: model.calls.Load(), Reason: reason}
	if output, ok := completed.Termination().Output(); ok {
		decoded, decodeErr := deployment.Descriptor().DecodeOutput[interaction.Output](output)
		if decodeErr != nil {
			return result, decodeErr
		}
		result.Message = decoded.ModelResponse.Text()
	}
	return result, nil
}

func unknownRequest(head agent.TreeSnapshot) (agent.EffectRequest, error) {
	var result agent.EffectRequest
	for _, snapshot := range head.ProcessSnapshots() {
		for _, id := range snapshot.UnknownEffectIDs() {
			if result.Valid() {
				return agent.EffectRequest{}, errors.New("example: expected exactly one Unknown")
			}
			request, found := head.EffectRequest(snapshot.Relation().ProcessID(), id)
			if !found {
				return agent.EffectRequest{}, errors.New("example: missing original Effect request")
			}
			result = request
		}
	}
	if !result.Valid() {
		return result, errors.New("example: no Unknown to investigate")
	}
	return result, nil
}

func reconcile(ctx context.Context, lookup func(context.Context, agent.EffectID) (receipt, bool, error), tools interaction.ToolSet, request agent.EffectRequest, owner *agent.Process) (bool, string, error) {
	outcome, found, err := lookup(ctx, request.ID())
	if err != nil {
		return false, err.Error(), nil
	}
	if !found {
		return false, "remote receipt not found; outcome unresolved", nil
	}
	invocation, err := interaction.ToolInvocationFromRequest(request)
	if err != nil {
		return false, "", err
	}
	var input note
	if decodeErr := jsonv2.Unmarshal([]byte(invocation.ToolCall().Arguments), &input, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
		return false, "", decodeErr
	}
	if outcome.EffectID != request.ID() || outcome.Note != input {
		return false, "", errors.New("example: remote receipt does not match the original write")
	}
	disposition, text := interaction.ResultRejected, "write denied"
	if outcome.Accepted {
		disposition, text = interaction.ResultSucceeded, "stored:"+outcome.Note.Text
	}
	settlement, err := tools.SettleToolResult(request, chat.NewTextToolOutput(text), disposition, nil)
	if err != nil {
		return false, "", err
	}
	if resolveErr := owner.ResolveUnknownEffect(ctx, request.ID(), settlement); resolveErr != nil {
		return false, "", resolveErr
	}
	return true, "", nil
}

// A replacement Engine fences its predecessor. Cleanup still joins both trees;
// only an already finished Process or the superseded writer is expected here.
func closeTree(ctx context.Context, engine *agent.Engine, root *agent.Process) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	if root != nil {
		if killErr := root.Kill(cleanup, "close example tree"); killErr != nil && !errors.Is(killErr, agent.ErrProcessFinished) && !errors.Is(killErr, agent.ErrTreeIncarnationConflict) {
			err = errors.Join(err, killErr)
		}
		if joinErr := root.Join(cleanup); joinErr != nil && !errors.Is(joinErr, agent.ErrTreeIncarnationConflict) {
			err = errors.Join(err, joinErr)
		}
	}
	return errors.Join(err, engine.Close(cleanup))
}
