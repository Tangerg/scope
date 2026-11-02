package main

import (
	"context"
	"crypto/rand"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

const (
	exampleBuild          = "example.reconciliation.v1"
	exampleTimeout        = 10 * time.Second
	runtimeCleanupTimeout = 5 * time.Second
)

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), exampleTimeout)
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
		result, err := runScenario(ctx, &remoteLedger{reject: scenario.reject, availability: scenario.availability})
		if err != nil {
			return fmt.Errorf("%s: %w", scenario.name, err)
		}
		inspection, err := result.Head.Inspect()
		if err != nil {
			return err
		}
		root, found := inspection.Process(inspection.Root)
		if !found {
			return errors.New("example: root absent from committed log")
		}
		unknown := unknownCount(inspection)
		status := root.Status.String()
		if unknown != 0 {
			status = "unresolved"
		}
		message, err := result.message()
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(output, "%s: %s unknown=%d writes=%d attempts=%d model_calls=%d result=%q reason=%q\n",
			scenario.name, status, unknown, result.Remote.Writes, result.Remote.Attempts,
			result.ModelCalls, message, result.Reason); err != nil {
			return err
		}
	}
	return nil
}

type scenarioResult struct {
	Before     agentrun.Log
	Head       agentrun.Log
	Request    agentrun.EffectRequest
	Remote     ledgerReport
	ModelCalls int32
	Reason     string
}

func (s scenarioResult) message() (string, error) {
	inspection, err := s.Head.Inspect()
	if err != nil {
		return "", err
	}
	root, found := inspection.Process(inspection.Root)
	if !found {
		return "", errors.New("example: missing root process")
	}
	payload, completed := root.Termination.Output()
	if !completed {
		return "", nil
	}
	output, err := payload.Decode[interaction.Output]()
	if err != nil {
		return "", err
	}
	if output.ModelResponse == nil {
		return "", errors.New("example: missing receipt report")
	}
	return output.ModelResponse.Text(), nil
}

func runScenario(ctx context.Context, remote *remoteLedger) (result scenarioResult, runErr error) {
	model := &receiptModel{}
	store := agentrun.NewMemoryStore()
	runtime, err := agentrun.New(ctx, agentrun.Config{Store: store, Build: exampleBuild})
	if err != nil {
		return result, err
	}
	defer func() { runErr = errors.Join(runErr, closeRuntime(ctx, runtime)) }()
	program, err := newProgram(runtime.Compiler(), remote, model)
	if err != nil {
		return result, err
	}
	root, err := runtime.Start(ctx, program, agentrun.StartRequest[interaction.Input]{
		ID:    agentrun.StartID{Scope: "example", Key: rand.Text()},
		Input: interaction.Input{Messages: []chat.Message{chat.NewUserMessage(chat.NewTextPart("Record one release note and report its receipt."))}},
	})
	if err != nil {
		return result, err
	}
	if observeErr := awaitUnknown(ctx, root); observeErr != nil {
		return result, observeErr
	}
	// Closing stops the local owner while retaining the unresolved external fact.
	if closeErr := closeRuntime(ctx, runtime); closeErr != nil {
		return result, closeErr
	}
	before, found, err := store.Load(ctx, root.ID())
	if err != nil || !found {
		return result, errors.Join(errors.New("example: no authoritative log"), err)
	}
	request, err := unknownRequest(before)
	if err != nil {
		return result, err
	}
	replacement, err := agentrun.New(ctx, agentrun.Config{Store: store, Build: exampleBuild})
	if err != nil {
		return result, err
	}
	defer func() { runErr = errors.Join(runErr, closeRuntime(ctx, replacement)) }()
	restoredProgram, err := newProgram(replacement.Compiler(), remote, model)
	if err != nil {
		return result, err
	}
	restored, err := replacement.Recover(ctx, restoredProgram, root.ID())
	if err != nil {
		return result, err
	}
	resolved, reason, err := reconcile(ctx, remote.lookup, restoredProgram, request, restored)
	if err != nil {
		return result, err
	}
	if resolved {
		if _, awaitErr := restored.Await(ctx); awaitErr != nil {
			return result, awaitErr
		}
	} else if cancelErr := restored.Cancel(ctx, reason); cancelErr != nil {
		return result, cancelErr
	}
	head, found, err := store.Load(ctx, root.ID())
	if err != nil || !found {
		return result, errors.Join(errors.New("example: missing final log"), err)
	}
	return scenarioResult{Before: before, Head: head, Request: request, Remote: remote.report(), ModelCalls: model.calls.Load(), Reason: reason}, nil
}

func awaitUnknown(ctx context.Context, root *agentrun.Handle[interaction.Output]) error {
	for entry, err := range root.Observe(ctx, 0) {
		if err != nil {
			return err
		}
		if outcome, ok := entry.Record.(agentrun.EffectSettled); ok && outcome.Outcome.Status() == agentrun.SettlementStatusUnknown {
			return nil
		}
	}
	return errors.New("example: interaction exited without its uncertain receipt")
}

func unknownCount(inspection agentrun.TreeInspection) int {
	count := 0
	for _, process := range inspection.Processes {
		for _, effect := range process.PendingEffects {
			if effect.Outcome.Status() == agentrun.SettlementStatusUnknown {
				count++
			}
		}
	}
	return count
}

func unknownRequest(log agentrun.Log) (agentrun.EffectRequest, error) {
	inspection, err := log.Inspect()
	if err != nil {
		return agentrun.EffectRequest{}, err
	}
	var result agentrun.EffectRequest
	for _, process := range inspection.Processes {
		for _, effect := range process.PendingEffects {
			if effect.Outcome.Status() != agentrun.SettlementStatusUnknown {
				continue
			}
			if result.Valid() {
				return agentrun.EffectRequest{}, errors.New("example: expected exactly one Unknown")
			}
			result = effect.Request
		}
	}
	if !result.Valid() {
		return result, errors.New("example: no Unknown to investigate")
	}
	return result, nil
}

func reconcile(ctx context.Context, lookup func(context.Context, string) (receipt, bool, error), program agentrun.Program[interaction.Input, interaction.Output], request agentrun.EffectRequest, owner *agentrun.Handle[interaction.Output]) (bool, string, error) {
	invocation, err := interaction.ToolInvocationFromRequest(request)
	if err != nil {
		return false, "", err
	}
	outcome, found, err := lookup(ctx, invocation.IdempotencyKey())
	if err != nil {
		return false, err.Error(), nil
	}
	if !found {
		return false, "remote receipt not found; outcome unresolved", nil
	}
	var input note
	if decodeErr := jsonv2.Unmarshal([]byte(invocation.ToolCall().Arguments), &input, jsonv2.RejectUnknownMembers(true)); decodeErr != nil {
		return false, "", decodeErr
	}
	if outcome.IdempotencyKey != invocation.IdempotencyKey() || outcome.Note != input {
		return false, "", errors.New("example: remote receipt does not match the original write")
	}
	disposition, text := interaction.ResultRejected, "write denied"
	if outcome.Accepted {
		disposition, text = interaction.ResultSucceeded, "stored:"+outcome.Note.Text
	}
	settlement, err := interaction.SettleToolResult(program, request, toolcontract.Result{Output: toolcontract.NewTextOutput(text)}, disposition, nil)
	if err != nil {
		return false, "", err
	}
	if err := owner.ResolveUnknownEffect(ctx, request.ID(), settlement); err != nil {
		return false, "", err
	}
	return true, "", nil
}

func closeRuntime(ctx context.Context, runtime *agentrun.Runtime) error {
	cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), runtimeCleanupTimeout)
	defer cancel()
	return runtime.Close(cleanup)
}
