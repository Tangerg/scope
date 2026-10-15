//go:build integration

package treecommitterconformance

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
)

const workerArgument = "tree-committer-worker"

type processPhase string

const (
	processCut       processPhase = "cut"
	processUnknown   processPhase = "unknown"
	processCompleted processPhase = "completed"
	processFenced    processPhase = "fenced"
)

type workerRequest struct {
	DSN             string          `json:"dsn"`
	Schema          string          `json:"schema"`
	RestoreRoot     agent.ProcessID `json:"restore_root,omitzero"`
	ReconcileEffect agent.EffectID  `json:"reconcile_effect,omitzero"`
	Cut             commitCut       `json:"cut,omitzero"`
	Sequence        uint64          `json:"sequence,omitzero"`
	ExpectFenced    bool            `json:"expect_fenced"`
}

type workerNotice struct {
	Phase    processPhase        `json:"phase"`
	RootID   agent.ProcessID     `json:"root_id"`
	Snapshot *agent.TreeSnapshot `json:"snapshot,omitzero"`
	EffectID agent.EffectID      `json:"effect_id,omitzero"`
	Output   *probeValue         `json:"output,omitzero"`
}

type workerCommand struct {
	Continue bool `json:"continue"`
}

type workerChannel struct {
	input   io.ReadCloser
	decoder *jsontext.Decoder
	output  io.Writer
}

func (w workerChannel) wait(ctx context.Context, notice workerNotice) error {
	if err := jsonv2.MarshalWrite(w.output, notice); err != nil {
		return err
	}
	decision := make(chan error, 1)
	go func() {
		var command workerCommand
		err := jsonv2.UnmarshalDecode(w.decoder, &command, jsonv2.RejectUnknownMembers(true))
		if err == nil && !command.Continue {
			err = errors.New("worker: explicit continue is required")
		}
		decision <- err
	}()
	select {
	case err := <-decision:
		return err
	case <-ctx.Done():
		closeErr := w.input.Close()
		<-decision
		return errors.Join(ctx.Err(), closeErr)
	}
}

func TestMain(m *testing.M) {
	if len(os.Args) == 2 && os.Args[1] == workerArgument {
		if err := runWorker(os.Stdin, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func runWorker(input io.ReadCloser, output io.Writer) (runErr error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	channel := workerChannel{input: input, decoder: jsontext.NewDecoder(input), output: output}
	var request workerRequest
	if err := jsonv2.UnmarshalDecode(channel.decoder, &request, jsonv2.RejectUnknownMembers(true)); err != nil {
		return err
	}
	pool, err := openPool(ctx, request.DSN, request.Schema)
	if err != nil {
		return err
	}
	defer pool.Close()
	committer := &postgresCommitter{pool: pool, shutdown: ctx, timeout: storageTimeout, business: []byte("accepted business write")}
	unknownCommitted := make(chan struct{}, 1)
	var notifyUnknown sync.Once
	committer.gate = func(gateContext context.Context, cut commitCut, write postgresWrite) error {
		if cut == cutAfterCommit && request.ReconcileEffect.Valid() {
			for _, snapshot := range write.snapshot.ProcessSnapshots() {
				if slices.Contains(snapshot.UnknownEffectIDs(), request.ReconcileEffect) {
					notifyUnknown.Do(func() { unknownCommitted <- struct{}{} })
				}
			}
		}
		if cut != request.Cut || write.sequence != request.Sequence {
			return nil
		}
		return channel.wait(gateContext, workerNotice{Phase: processCut, RootID: write.snapshot.RootID(), Snapshot: new(write.snapshot)})
	}
	dispatcher := probeDispatcher{pool: pool}
	if request.Cut == cutAfterDispatch {
		dispatcher.gate = func(dispatchContext context.Context, effect agent.EffectRequest) error {
			return channel.wait(dispatchContext, workerNotice{Phase: processCut, RootID: effect.Relation().RootID(), EffectID: effect.ID()})
		}
	}
	deployment, err := probeDeployment(dispatcher)
	if err != nil {
		return err
	}
	engine, err := agent.NewEngine(agent.EngineConfig{TreeCommitter: committer})
	if err != nil {
		return err
	}
	defer func() {
		cleanupContext, cleanupCancel := context.WithTimeout(context.Background(), storageTimeout)
		defer cleanupCancel()
		runErr = errors.Join(runErr, engine.Close(cleanupContext))
	}()
	var process *agent.Process
	if request.RestoreRoot.Valid() {
		head, exists, loadErr := committer.LoadTree(ctx, request.RestoreRoot)
		if loadErr != nil || !exists {
			return errors.Join(errors.New("worker: recovery requires an acknowledged head"), loadErr)
		}
		process, err = engine.RestoreTree(ctx, deployment, head)
	} else {
		payload, encodeErr := agent.EncodePayload(probeValue{Value: "durable"})
		if encodeErr != nil {
			return encodeErr
		}
		process, err = engine.Start(ctx, deployment, payload)
	}
	if err != nil {
		return err
	}
	if request.RestoreRoot.Valid() {
		if request.ReconcileEffect.Valid() {
			select {
			case <-unknownCommitted:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		head, captureErr := engine.CaptureTree(ctx, process.Relation().ProcessID())
		if captureErr != nil {
			return captureErr
		}
		for _, snapshot := range head.ProcessSnapshots() {
			for _, id := range snapshot.UnknownEffectIDs() {
				if decisionErr := channel.wait(ctx, workerNotice{Phase: processUnknown, RootID: head.RootID(), Snapshot: &head, EffectID: id}); decisionErr != nil {
					return decisionErr
				}
				if replayErr := process.ReplayUnknownEffect(ctx, id); !errors.Is(replayErr, agent.ErrEffectReplayForbidden) {
					return errors.Join(errors.New("worker: expected forbidden replay"), replayErr)
				}
				settlement, settleErr := reconcileProbe(ctx, pool, id)
				if settleErr != nil {
					return settleErr
				}
				original, found := head.EffectRequest(snapshot.Relation().ProcessID(), id)
				if !found || !bytes.Equal(original.Effect().Payload(), settlement.Payload()) {
					return errors.New("worker: receipt does not match the authoritative Effect request")
				}
				if resolveErr := process.ResolveUnknownEffect(ctx, id, settlement); resolveErr != nil {
					return resolveErr
				}
			}
		}
	}
	result, err := process.Await(ctx)
	if request.ExpectFenced {
		if !errors.Is(err, agent.ErrTreeIncarnationConflict) {
			return errors.Join(errors.New("worker: expected writer fencing"), err)
		}
		return jsonv2.MarshalWrite(output, workerNotice{Phase: processFenced, RootID: process.Relation().ProcessID()})
	}
	if err != nil {
		return err
	}
	if joinErr := process.Join(ctx); joinErr != nil {
		return joinErr
	}
	payload, completed := result.Termination().Output()
	if !completed || result.Termination().Status() != agent.StatusCompleted {
		return errors.New("worker: expected completed output")
	}
	value, err := payload.Decode[probeValue]()
	if err != nil {
		return err
	}
	return jsonv2.MarshalWrite(output, workerNotice{Phase: processCompleted, RootID: process.Relation().ProcessID(), Output: &value})
}

type workerProcess struct {
	command *exec.Cmd
	input   io.WriteCloser
	decoder *jsontext.Decoder
	stderr  bytes.Buffer
	done    chan struct{}
	waitErr error
}

func startWorker(t *testing.T, fixture postgresFixture, request workerRequest) *workerProcess {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.CommandContext(t.Context(), executable, workerArgument)
	input, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := input.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
			t.Error(closeErr)
		}
	})
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, file := range []*os.File{reader, writer} {
			if closeErr := file.Close(); closeErr != nil && !errors.Is(closeErr, os.ErrClosed) {
				t.Error(closeErr)
			}
		}
	})
	process := &workerProcess{command: command, input: input, decoder: jsontext.NewDecoder(reader), done: make(chan struct{})}
	command.Stdout, command.Stderr = writer, &process.stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	go func() {
		process.waitErr = command.Wait()
		close(process.done)
	}()
	t.Cleanup(func() {
		select {
		case <-process.done:
		default:
			if killErr := command.Process.Kill(); killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
				t.Error(killErr)
			}
			<-process.done
		}
	})
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request.DSN, request.Schema = fixture.dsn, fixture.schema
	if err := jsonv2.MarshalWrite(input, request); err != nil {
		t.Fatal(err)
	}
	return process
}

func (w *workerProcess) next(t *testing.T) workerNotice {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	type readResult struct {
		notice workerNotice
		err    error
	}
	results := make(chan readResult, 1)
	go func() {
		var notice workerNotice
		err := jsonv2.UnmarshalDecode(w.decoder, &notice, jsonv2.RejectUnknownMembers(true))
		results <- readResult{notice: notice, err: err}
	}()
	select {
	case result := <-results:
		if result.err != nil {
			w.stop(t)
			t.Fatalf("worker output: %v; stderr: %s", result.err, w.stderr.String())
		}
		return result.notice
	case <-ctx.Done():
		w.stop(t)
		<-results
		t.Fatalf("worker did not signal its cut: %v; stderr: %s", ctx.Err(), w.stderr.String())
		return workerNotice{}
	}
}

func (w *workerProcess) send(t *testing.T) {
	t.Helper()
	if err := jsonv2.MarshalWrite(w.input, workerCommand{Continue: true}); err != nil {
		t.Fatal(err)
	}
}

func (w *workerProcess) stop(t *testing.T) {
	t.Helper()
	select {
	case <-w.done:
	default:
		if err := w.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
			t.Fatal(err)
		}
		<-w.done
	}
}

func (w *workerProcess) crash(t *testing.T) {
	t.Helper()
	select {
	case <-w.done:
		t.Fatalf("worker exited before the crash cut: %v; stderr: %s", w.waitErr, w.stderr.String())
	default:
	}
	if err := w.command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	<-w.done
	if _, killed := errors.AsType[*exec.ExitError](w.waitErr); !killed {
		t.Fatalf("worker did not report abnormal termination: %v", w.waitErr)
	}
}

func (w *workerProcess) finish(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	select {
	case <-w.done:
		if w.waitErr != nil {
			t.Fatalf("worker failed: %v; stderr: %s", w.waitErr, w.stderr.String())
		}
	case <-ctx.Done():
		w.stop(t)
		t.Fatalf("worker did not finish: %v", ctx.Err())
	}
}

func assertDatabaseCounts(t *testing.T, committer *postgresCommitter, heads, facts, business, calls int) {
	t.Helper()
	var actualHeads, actualFacts, actualBusiness, actualCalls int
	if err := committer.pool.QueryRow(t.Context(), "SELECT (SELECT count(*) FROM tree_heads),(SELECT count(*) FROM commit_facts),(SELECT count(*) FROM business_records),(SELECT count(*) FROM external_calls)").Scan(&actualHeads, &actualFacts, &actualBusiness, &actualCalls); err != nil {
		t.Fatal(err)
	}
	if actualHeads != heads || actualFacts != facts || actualBusiness != business || actualCalls != calls {
		t.Fatalf("database counts: heads=%d facts=%d business=%d dispatches=%d; want %d %d %d %d", actualHeads, actualFacts, actualBusiness, actualCalls, heads, facts, business, calls)
	}
}

func assertCompletedWorker(t *testing.T, process *workerProcess, committer *postgresCommitter, expectedRoot agent.ProcessID) agent.TreeSnapshot {
	t.Helper()
	notice := process.next(t)
	if notice.Phase != processCompleted || notice.Output == nil || notice.Output.Value != "durable" || expectedRoot.Valid() && notice.RootID != expectedRoot {
		t.Fatalf("unexpected worker completion: %+v", notice)
	}
	process.finish(t)
	head, exists, err := committer.LoadTree(t.Context(), notice.RootID)
	if err != nil || !exists || head.ProcessSnapshots()[0].Status() != agent.StatusCompleted {
		t.Fatalf("completion was not durably published: exists=%t error=%v", exists, err)
	}
	return head
}

func TestProcessCrashAtomicStart(t *testing.T) {
	for _, cut := range []commitCut{cutBeforeCommit, cutAfterCommit} {
		t.Run(string(cut), func(t *testing.T) {
			fixture := newPostgresFixture(t)
			original := startWorker(t, fixture, workerRequest{Cut: cut, Sequence: 1})
			notice := original.next(t)
			if notice.Phase != processCut || notice.Snapshot == nil {
				t.Fatalf("unexpected crash cut: %+v", notice)
			}
			head, exists, err := fixture.committer.LoadTree(t.Context(), notice.RootID)
			if err != nil || exists != (cut == cutAfterCommit) || exists && head.Digest() != notice.Snapshot.Digest() {
				t.Fatalf("unacknowledged transaction visibility: exists=%t error=%v", exists, err)
			}
			count := 0
			if cut == cutAfterCommit {
				count = 1
			}
			assertDatabaseCounts(t, fixture.committer, count, count, count, 0)
			original.crash(t)
			assertDatabaseCounts(t, fixture.committer, count, count, count, 0)
			var restore agent.ProcessID
			if cut == cutAfterCommit {
				restore = notice.RootID
			}
			replacement := startWorker(t, fixture, workerRequest{RestoreRoot: restore})
			completed := assertCompletedWorker(t, replacement, fixture.committer, restore)
			if cut == cutAfterCommit && completed.IncarnationID() == notice.Snapshot.IncarnationID() {
				t.Fatal("recovery retained the dead writer")
			}
			var calls int
			if err := fixture.committer.pool.QueryRow(t.Context(), "SELECT count(*) FROM external_calls").Scan(&calls); err != nil || calls != 1 {
				t.Fatalf("recovery dispatch count=%d error=%v", calls, err)
			}
		})
	}
}

func TestProcessCrashAfterDispatch(t *testing.T) {
	fixture := newPostgresFixture(t)
	original := startWorker(t, fixture, workerRequest{Cut: cutAfterDispatch})
	notice := original.next(t)
	if notice.Phase != processCut || !notice.EffectID.Valid() {
		t.Fatalf("unexpected dispatch cut: %+v", notice)
	}
	before, exists, err := fixture.committer.LoadTree(t.Context(), notice.RootID)
	if err != nil || !exists {
		t.Fatalf("missing dispatch permission: %t %v", exists, err)
	}
	original.crash(t)
	replacement := startWorker(t, fixture, workerRequest{RestoreRoot: notice.RootID, ReconcileEffect: notice.EffectID})
	unknown := replacement.next(t)
	if unknown.Phase != processUnknown || unknown.EffectID != notice.EffectID || unknown.Snapshot == nil || unknown.Snapshot.IncarnationID() == before.IncarnationID() {
		t.Fatalf("uncertain execution was not retained: %+v", unknown)
	}
	var calls int
	if err := fixture.committer.pool.QueryRow(t.Context(), "SELECT count(*) FROM external_calls").Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("recovery repeated external dispatch: calls=%d error=%v", calls, err)
	}
	replacement.send(t)
	completed := assertCompletedWorker(t, replacement, fixture.committer, notice.RootID)
	if len(completed.ProcessSnapshots()[0].UnknownEffectIDs()) != 0 {
		t.Fatal("resolution did not reach the authoritative head")
	}
	if err := fixture.committer.pool.QueryRow(t.Context(), "SELECT count(*) FROM external_calls").Scan(&calls); err != nil || calls != 1 {
		t.Fatalf("reconciliation replayed the Effect: calls=%d error=%v", calls, err)
	}
}

func TestProcessWriterFencing(t *testing.T) {
	fixture := newPostgresFixture(t)
	original := startWorker(t, fixture, workerRequest{Cut: cutBeforeTransaction, Sequence: 2, ExpectFenced: true})
	notice := original.next(t)
	if notice.Phase != processCut || notice.Snapshot == nil {
		t.Fatalf("unexpected writer cut: %+v", notice)
	}
	replacement := startWorker(t, fixture, workerRequest{RestoreRoot: notice.RootID})
	completed := assertCompletedWorker(t, replacement, fixture.committer, notice.RootID)
	original.send(t)
	if fenced := original.next(t); fenced.Phase != processFenced {
		t.Fatalf("stale writer was not fenced: %+v", fenced)
	}
	original.finish(t)
	final, exists, err := fixture.committer.LoadTree(t.Context(), notice.RootID)
	if err != nil || !exists || final.Digest() != completed.Digest() {
		t.Fatalf("old writer changed the replacement head: exists=%t error=%v", exists, err)
	}
}
