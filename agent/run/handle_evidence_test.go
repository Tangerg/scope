package run

import (
	"context"
	"errors"
	"iter"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/core/jsonschema"
)

func TestHandleEvidenceSurvivesConsumerOverflowAndAnswerRetry(t *testing.T) {
	spec := controlValue(NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("approval")), AnswerSchema: controlValue(jsonschema.For[string]()), MaxAnswerBytes: 128, AnswerIsOutput: true}))
	rt, h, _ := startExternalWaitRestoreProcess(t, spec)
	wait, ok := inspectProcessSnapshot(t, h).WaitID()
	if !ok {
		t.Fatal("expected external wait")
	}
	// A zero cursor first attaches to the actual acknowledged tree. Pausing the
	// iterator here simulates a consumer blocked on a UI write, not a paused run.
	next, stop := iter.Pull2(h.Observe(t.Context(), Cursor{}, 1))
	defer stop()
	initial, err, ok := next()
	if !ok || err != nil || !initial.Snapshot.Valid() || initial.Cursor.RootID != h.ID() {
		t.Fatal("initial", initial.Cursor, err)
	}
	value := controlValue(EncodePayload("approved"))
	if found, answeredErr := initial.Snapshot.Answered(wait, value); found || answeredErr != nil {
		t.Fatal("wait falsely answered", answeredErr)
	}
	if answerErr := h.Answer(t.Context(), wait, func() {}); answerErr == nil {
		t.Fatal("unencodable answer admitted")
	}
	if answerErr := h.Answer(t.Context(), WaitID{}, "approved"); !errors.Is(answerErr, ErrSignalRejected) {
		t.Fatal(answerErr)
	}
	if answerErr := h.Answer(t.Context(), wait, "approved"); answerErr != nil {
		t.Fatal(answerErr)
	}
	if joinErr := h.Join(t.Context()); joinErr != nil {
		t.Fatal(joinErr)
	}
	if answerErr := h.Answer(t.Context(), wait, "approved"); answerErr != nil {
		t.Fatal("retry changed accepted result:", answerErr)
	}
	if answerErr := h.Answer(t.Context(), wait, "denied"); !errors.Is(answerErr, ErrSignalConflict) {
		t.Fatal("conflicting retry", answerErr)
	}
	update, err, ok := next()
	if !ok || err != nil || update.Cursor.Position <= initial.Cursor.Position {
		t.Fatalf("missing retained change: %v %v", update.Cursor, err)
	}
	if _, err, ok = next(); !ok || !errors.Is(err, ErrObservationGap) {
		t.Fatalf("silent gap: ok=%v err=%v", ok, err)
	}
	if _, _, ok = next(); ok {
		t.Fatal("overflowed reader resumed without resync")
	}
	requireObservationError(t, h.Observe(t.Context(), initial.Cursor, 4), ErrObservationGap)
	var terminal Observation
	for view, err := range h.Observe(t.Context(), Cursor{}, 4) {
		if err != nil {
			t.Fatal(err)
		}
		if terminal.Snapshot.Valid() {
			t.Fatal("terminal stream emitted a second snapshot")
		}
		terminal = view
	}
	if !terminal.Snapshot.Valid() || terminal.Cursor.Position <= update.Cursor.Position {
		t.Fatal("resync missed terminal head")
	}
	if found, err := terminal.Snapshot.Answered(wait, value); !found || err != nil {
		t.Fatal("receipt missing", err)
	}
	if found, err := terminal.Snapshot.Answered(wait, controlValue(EncodePayload("denied"))); found || !errors.Is(err, ErrSignalConflict) {
		t.Fatal("conflicting receipt", err)
	}
	if _, err := terminal.Snapshot.Answered(WaitID{}, value); !errors.Is(err, ErrSignalRejected) {
		t.Fatal(err)
	}
	for _, err := range h.Observe(t.Context(), terminal.Cursor, 4) {
		t.Fatal("exact terminal cursor replayed data", err)
	}
	if count, err := h.FlushObservations(t.Context()); count == 0 || err != nil {
		t.Fatalf("framework event watermark=%d err=%v", count, err)
	}
	if err := rt.ReleaseTree(t.Context(), h.ID()); err != nil {
		t.Fatal(err)
	}
	requireObservationError(t, h.Observe(t.Context(), Cursor{}, 4), ErrTreeNotFound)
	if _, err := h.FlushObservations(t.Context()); !errors.Is(err, ErrTreeNotFound) {
		t.Fatal(err)
	}
	if got := mustAwait(t, h).Termination().Status(); got != StatusCompleted {
		t.Fatal("release changed result", got)
	}
}

func requireObservationError(t *testing.T, seq iter.Seq2[Observation, error], want error) {
	t.Helper()
	count := 0
	for o, err := range seq {
		count++
		if !errors.Is(err, want) || o.Snapshot.Valid() || o.Event.Valid() {
			t.Fatalf("unexpected observation %#v, %v", o, err)
		}
	}
	if count != 1 {
		t.Fatalf("errors emitted=%d", count)
	}
}
func TestObservationAdmissionRejectsUnusableConsumer(t *testing.T) {
	var h *Handle[Payload]
	requireObservationError(t, h.Observe(t.Context(), Cursor{}, 0), ErrObservationCapacity)
	requireObservationError(t, h.Observe(t.Context(), Cursor{}, 1), ErrTreeNotFound)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	requireObservationError(t, h.Observe(ctx, Cursor{}, 1), context.Canceled)
	if _, err := h.FlushObservations(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := h.FlushObservations(t.Context()); !errors.Is(err, ErrTreeNotFound) {
		t.Fatal(err)
	}
}

type evidenceReadStore struct {
	Store
	acceptance func(context.Context, StartID) (StartReceipt, bool, error)
	head       func(context.Context, ProcessID) (TreeSnapshot, bool, error)
}

func (e evidenceReadStore) LookupStart(ctx context.Context, id StartID) (StartReceipt, bool, error) {
	return e.acceptance(ctx, id)
}
func (e evidenceReadStore) LoadTree(ctx context.Context, id ProcessID) (TreeSnapshot, bool, error) {
	return e.head(ctx, id)
}
func TestStartedTreeEvidenceFailsClosedOnLostOrCrossScopedState(t *testing.T) {
	b, _, _ := treeCommitIdentityFixture(t)
	head := b.TreeSnapshot()
	id := StartID{Scope: "tenant-A", Key: "same-key"}
	receipt := StartReceipt{ID: id, Intent: ComputeDigest([]byte("intent")), RootID: head.RootID()}
	fault := errors.New("head service offline")
	for _, mode := range []string{"wrong scope", "bad receipt", "missing head", "invalid head", "other root", "lookup timeout", "lookup panic", "head timeout", "head fault", "invalid key"} {
		t.Run(mode, func(t *testing.T) {
			reads := 0
			st := evidenceReadStore{Store: NewMemoryStore(), acceptance: func(ctx context.Context, asked StartID) (StartReceipt, bool, error) {
				if asked != id {
					t.Fatalf("leaked scope %v", asked)
				}
				r := receipt
				switch mode {
				case "wrong scope":
					r.ID.Scope = "tenant-B"
				case "bad receipt":
					r.Intent = Digest{}
				case "other root":
					r.RootID = controlValue(ParseProcessID("proc-other"))
				case "lookup panic":
					panic(fault)
				case "lookup timeout":
					return StartReceipt{}, false, ctx.Err()
				}
				return r, true, nil
			}, head: func(ctx context.Context, root ProcessID) (TreeSnapshot, bool, error) {
				reads++
				switch mode {
				case "missing head":
					return TreeSnapshot{}, false, nil
				case "invalid head":
					return TreeSnapshot{}, true, nil
				case "head fault":
					return TreeSnapshot{}, false, fault
				case "head timeout":
					return TreeSnapshot{}, false, ctx.Err()
				}
				return head, true, nil
			}}
			rt := controlValue(New(t.Context(), Config{Store: st}))
			defer mustCloseEngine(t, rt)
			v := controlValue(rt.StartedTrees())
			ctx := t.Context()
			if mode == "lookup timeout" || mode == "head timeout" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Unix(0, 0))
				defer cancel()
			}
			key := id
			if mode == "invalid key" {
				key.Key = ""
			}
			got, found, err := v.Load(ctx, key)
			if found || got.Valid() || err == nil {
				t.Fatalf("published bad head=%v found=%v err=%v", got.RootID(), found, err)
			}
			switch mode {
			case "lookup timeout", "head timeout":
				if !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case "lookup panic":
				callback, ok := errors.AsType[*CallbackPanicError](err)
				if !ok || callback.Operation != "Store.LookupStart" || reads != 0 || !errors.Is(err, fault) {
					t.Fatalf("lookup panic escaped its boundary: reads=%d err=%v", reads, err)
				}
			case "head fault":
				if !errors.Is(err, fault) {
					t.Fatal(err)
				}
			case "wrong scope", "bad receipt", "invalid key":
				if reads != 0 || !errors.Is(err, ErrInvalidStart) {
					t.Fatalf("head reads=%d err=%v", reads, err)
				}
			}
			if _, ok := rt.Process(head.RootID()); ok {
				t.Fatal("read installed a writer")
			}
		})
	}
	var nilRuntime *Runtime
	if _, err := nilRuntime.StartedTrees(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatal(err)
	}
	type writeOnly struct{ Store }
	r := controlValue(New(t.Context(), Config{Store: writeOnly{NewMemoryStore()}}))
	defer mustCloseEngine(t, r)
	if _, err := r.StartedTrees(); !errors.Is(err, errors.ErrUnsupported) {
		t.Fatal(err)
	}
}

func TestCurrentCursorResumesAndSubscriberCancellationLeavesWriterRunning(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		spec := controlValue(NewExternalWaitSpec(ExternalWaitSpecConfig{Key: controlValue(ParseWaitKey("approve")), AnswerSchema: controlValue(jsonschema.For[string]()), MaxAnswerBytes: 128, AnswerIsOutput: true}))
		_, h, _ := startExternalWaitRestoreProcess(t, spec)
		var cursor Cursor
		for o, err := range h.Observe(t.Context(), Cursor{}, 8) {
			if err != nil {
				t.Fatal(err)
			}
			cursor = o.Cursor
			break
		}
		if !cursor.RootID.Valid() {
			t.Fatal("no acknowledged head")
		}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		done := make(chan error, 1)
		go func() {
			for _, err := range h.Observe(ctx, cursor, 8) {
				if err == nil {
					done <- errors.New("exact cursor replayed historical data while writer idle")
					return
				}
				done <- err
				return
			}
			done <- errors.New("consumer closed silently")
		}()
		synctest.Wait()
		select {
		case err := <-done:
			t.Fatal(err)
		default:
		}
		cancel()
		if err := <-done; !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
		wait, found := inspectProcessSnapshot(t, h).WaitID()
		if !found {
			t.Fatal("subscriber revoked wait")
		}
		view, err := h.Await(ctx)
		if !errors.Is(err, context.Canceled) || view.Termination().Status() == StatusCompleted {
			t.Fatalf("canceled waiter got %v %v", view, err)
		}
		if err := h.Answer(t.Context(), wait, "approve"); err != nil {
			t.Fatal(err)
		}
		if got := mustAwait(t, h).Termination().Status(); got != StatusCompleted {
			t.Fatal(got)
		}
	})
}

func TestHandleWatermarkJoinsExistingCallbacksWithoutChangingTheirCount(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		entered, release := make(chan struct{}), make(chan struct{})
		var once sync.Once
		var delivered atomic.Uint64
		var active atomic.Pointer[Handle[Payload]]
		var reentrant atomic.Bool
		listener := EventListenerFunc(func(ctx context.Context, _ Event) {
			once.Do(func() { close(entered); <-release })
			if h := active.Load(); h != nil {
				if _, err := h.FlushObservations(ctx); err != nil {
					reentrant.Store(true)
				}
			}
			delivered.Add(1)
		})
		rt := controlValue(New(t.Context(), Config{Store: NewMemoryStore(), EventListeners: []EventListener{listener}}))
		defer mustCloseEngine(t, rt)
		h := controlValue(rt.Start(t.Context(), newChildTestDeployment(t), StartRequest[Payload]{ID: StartID{Scope: "audit", Key: "watermark"}, Input: controlValue(EncodePayload(childTestInput{Mode: "leaf"}))}))
		active.Store(h)
		<-entered
		if e := h.Join(t.Context()); e != nil {
			t.Fatal(e)
		}
		ctx, cancel := context.WithCancel(t.Context())
		pending := make(chan error, 1)
		go func() { _, e := h.FlushObservations(ctx); pending <- e }()
		synctest.Wait()
		select {
		case e := <-pending:
			close(release)
			t.Fatalf("returned before accepted callback: %v", e)
		default:
		}
		cancel()
		if e := <-pending; !errors.Is(e, context.Canceled) {
			close(release)
			t.Fatal(e)
		}
		close(release)
		n, err := h.FlushObservations(t.Context())
		if err != nil || n == 0 || n != delivered.Load() || !reentrant.Load() {
			t.Fatalf("published=%d delivered=%d reentrant=%t err=%v", n, delivered.Load(), reentrant.Load(), err)
		}
	})
}

func TestHandleCannotAnswerManagedWaitInItsTreeOrAncestor(t *testing.T) {
	rt := controlValue(New(t.Context(), Config{Store: NewMemoryStore()}))
	defer mustCloseEngine(t, rt)
	root, child, grandchild := startWaitingSubtreeInEngine(t, rt, newChildTestDeployment(t))
	defer func() {
		if err := root.Cancel(context.WithoutCancel(t.Context()), "validation complete"); err != nil {
			t.Error(err)
		}
		if err := root.Join(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	}()
	wait, ok := inspectProcessSnapshot(t, root).WaitID()
	if !ok {
		t.Fatal("parent lacks managed wait")
	}
	other := controlValue(ParseWaitID("unrelated-wait"))
	for _, row := range []struct {
		owner *Handle[Payload]
		wait  WaitID
	}{{root, wait}, {child, wait}, {grandchild, wait}, {child, other}} {
		if err := row.owner.Answer(t.Context(), row.wait, "spoofed"); !errors.Is(err, ErrSignalRejected) {
			t.Fatalf("actor=%v wait=%v err=%v", row.owner.ID(), row.wait, err)
		}
	}
	now := inspectProcessSnapshot(t, root)
	if open, ok := now.WaitID(); !ok || open != wait || now.Status() != StatusWaiting {
		t.Fatal("rejected host reply advanced a child-owned wait")
	}
}
