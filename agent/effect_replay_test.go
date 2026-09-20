package agent

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"testing/synctest"
)

type replayTestDispatcher struct {
	policy   ReplayPolicy
	dispatch func(context.Context, EffectRequest) (Settlement, error)
}

func (r replayTestDispatcher) ReplayPolicy(Effect) ReplayPolicy { return r.policy }
func (r replayTestDispatcher) Dispatch(ctx context.Context, request EffectRequest, _ DeltaEmitter) (Settlement, error) {
	return r.dispatch(ctx, request)
}

func TestReplayUnknownEffectRetainsEvidenceAndSerializesResolution(t *testing.T) {
	for _, durable := range []bool{false, true} {
		for _, outcome := range []string{"success", "unknown", "canceled_success", "canceled_unknown"} {
			t.Run(fmt.Sprintf("durable_%t/%s", durable, outcome), func(t *testing.T) {
				synctest.Test(t, func(t *testing.T) {
					var calls int
					var original EffectRequest
					entered, release := make(chan struct{}), make(chan struct{})
					unblock := sync.OnceFunc(func() { close(release) })
					defer unblock()
					dispatcher := replayTestDispatcher{policy: ReplayPolicySameIdentity, dispatch: func(ctx context.Context, request EffectRequest) (Settlement, error) {
						calls++
						if calls == 1 {
							original = request
							return Settlement{}, errors.New("first attempt uncertain")
						}
						if request.ID() != original.ID() || request.Relation() != original.Relation() || request.StepSequence() != original.StepSequence() || request.BatchIndex() != original.BatchIndex() || !bytes.Equal(request.Effect().Payload(), original.Effect().Payload()) {
							return Settlement{}, errors.New("replay changed immutable intent")
						}
						if calls == 2 {
							close(entered)
							<-release
							if outcome == "canceled_success" || outcome == "canceled_unknown" {
								if ctx.Err() == nil {
									return Settlement{}, errors.New("cancellation did not reach owned replay")
								}
							}
							if outcome == "unknown" || outcome == "canceled_unknown" {
								return Settlement{}, errors.New("still uncertain")
							}
						}
						return NewSettlement(request.ID(), SettlementStatusSucceeded, []byte(`{"kind":"result","value":"confirmed"}`))
					}}
					config := EngineConfig{TreeCommitter: NewMemoryTreeCommitter()}
					store := &recordingTreeCommitter{}
					if durable {
						config.TreeCommitter = store
					}
					engine, err := NewEngine(config)
					if err != nil {
						t.Fatal(err)
					}
					defer mustCloseEngine(t, engine)
					deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
					input, _ := EncodePayload(engineTestInput{Value: "original"})
					process, err := engine.Start(t.Context(), deployment, input)
					if err != nil {
						t.Fatal(err)
					}
					synctest.Wait()
					ids := inspectProcessSnapshot(t, process).UnknownEffectIDs()
					if len(ids) != 1 || calls != 1 {
						t.Fatalf("initial ids=%v calls=%d", ids, calls)
					}
					replayed := make(chan error, 1)
					go func() { replayed <- process.ReplayUnknownEffect(t.Context(), ids[0]) }()
					<-entered
					synctest.Wait()
					if current := inspectProcessSnapshot(t, process).UnknownEffectIDs(); len(current) != 1 || current[0] != ids[0] {
						t.Fatal("replay erased prior uncertainty")
					}
					if replayErr := process.ReplayUnknownEffect(t.Context(), ids[0]); !errors.Is(replayErr, ErrEffectNotPending) {
						t.Fatalf("concurrent replay: %v", replayErr)
					}
					settlement, _ := NewSettlement(ids[0], SettlementStatusSucceeded, []byte(`{"kind":"result","value":"forged"}`))
					if resolveErr := process.ResolveUnknownEffect(t.Context(), settlement); !errors.Is(resolveErr, ErrEffectNotPending) {
						t.Fatalf("concurrent resolution: %v", resolveErr)
					}
					canceled := outcome == "canceled_success" || outcome == "canceled_unknown"
					if canceled {
						if killErr := process.Kill(t.Context(), "stop replay"); killErr != nil {
							t.Fatal(killErr)
						}
						synctest.Wait()
						if replayErr := process.ReplayUnknownEffect(t.Context(), ids[0]); !errors.Is(replayErr, ErrProcessFinished) {
							t.Fatalf("replay after terminal intent: %v", replayErr)
						}
					}
					unblock()
					err = <-replayed
					uncertain := outcome == "unknown" || outcome == "canceled_unknown"
					if uncertain {
						if !errors.Is(err, ErrEffectOutcomeUnknown) {
							t.Fatalf("uncertain replay: %v", err)
						}
						if current := inspectProcessSnapshot(t, process).UnknownEffectIDs(); len(current) != 1 || current[0] != ids[0] {
							t.Fatal("uncertain replay lost evidence")
						}
						if !canceled {
							if calls != 2 {
								t.Fatal("unknown was retried implicitly")
							}
							if replayErr := process.ReplayUnknownEffect(t.Context(), ids[0]); replayErr != nil {
								t.Fatal(replayErr)
							}
						}
					} else if err != nil {
						t.Fatal(err)
					}
					result := mustAwait(t, process)
					if joinErr := process.Join(t.Context()); joinErr != nil {
						t.Fatal(joinErr)
					}
					if canceled {
						if result.Status() != StatusKilled {
							t.Fatalf("status=%s", result.Status())
						}
					} else {
						if result.Status() != StatusCompleted {
							t.Fatalf("status=%s", result.Status())
						}
						output, _ := result.Output()
						decoded, _ := output.Decode[engineTestOutput]()
						if decoded.Value != "confirmed" {
							t.Fatalf("output=%+v", decoded)
						}
					}
					finalIDs := inspectProcessSnapshot(t, process).UnknownEffectIDs()
					if (len(finalIDs) != 0) != (canceled && uncertain) {
						t.Fatalf("final unknown=%v", finalIDs)
					}
					if result.Usage().PreparedEffects != 1 {
						t.Fatalf("replay spent another logical effect: %+v", result.Usage())
					}
					if durable {
						boundaries := store.effectBoundaries()
						want := 3
						if canceled && uncertain {
							want = 2
						}
						if len(boundaries) != want {
							t.Fatalf("boundaries=%d want=%d", len(boundaries), want)
						}
						if want == 3 && boundaries[2].Kind() != EffectBoundaryKindResolved {
							t.Fatal("replay did not commit definite resolution")
						}
					}
				})
			})
		}
	}
}

func TestReplayUnknownEffectRequiresSameIdentityPolicy(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		dispatcher := replayTestDispatcher{policy: ReplayPolicyNever, dispatch: func(context.Context, EffectRequest) (Settlement, error) {
			calls++
			return Settlement{}, errors.New("uncertain")
		}}
		engine, err := NewEngine(EngineConfig{TreeCommitter: NewMemoryTreeCommitter()})
		if err != nil {
			t.Fatal(err)
		}
		defer mustCloseEngine(t, engine)
		deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), dispatcher)
		input, _ := EncodePayload(engineTestInput{Value: "original"})
		process, err := engine.Start(t.Context(), deployment, input)
		if err != nil {
			t.Fatal(err)
		}
		synctest.Wait()
		ids := inspectProcessSnapshot(t, process).UnknownEffectIDs()
		if len(ids) != 1 {
			t.Fatal(ids)
		}
		if replayErr := process.ReplayUnknownEffect(t.Context(), ids[0]); !errors.Is(replayErr, ErrEffectReplayForbidden) {
			t.Fatalf("unsafe replay: %v", replayErr)
		}
		if replayErr := process.ReplayUnknownEffect(t.Context(), EffectID{}); !errors.Is(replayErr, ErrEffectNotPending) {
			t.Fatalf("invalid identity: %v", replayErr)
		}
		if calls != 1 {
			t.Fatalf("unsafe dispatches=%d", calls)
		}
		if killErr := process.Kill(t.Context(), "test complete"); killErr != nil {
			t.Fatal(killErr)
		}
		mustAwait(t, process)
		if joinErr := process.Join(t.Context()); joinErr != nil {
			t.Fatal(joinErr)
		}
	})
}
