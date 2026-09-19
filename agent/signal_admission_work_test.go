package agent

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func BenchmarkSignalAdmissionHistory(b *testing.B) {
	for _, history := range []int{10, 1000, 10000} {
		for _, replay := range []bool{false, true} {
			b.Run(fmt.Sprintf("history_%d/replay_%t", history, replay), func(b *testing.B) {
				process := admissionTestProcess(b, history)
				signal := mustMailboxSignal(b, "signal:next", WaitID{}, json.RawMessage(`{"value":"next"}`))
				if replay {
					signal = mustMailboxSignal(b, "signal:0", WaitID{}, json.RawMessage(`{}`))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for b.Loop() {
					accepted, err := admitTestSignals(process, []Signal{signal}, signalSourceExternal)
					if err != nil || accepted == replay {
						b.Fatalf("admission = %t, %v", accepted, err)
					}
					if !replay {
						delete(process.mailbox.seen, signal.ID())
						process.mailbox.records = process.mailbox.records[:history]
					}
				}
			})
		}
	}
}

func admissionTestProcess(t testing.TB, history int) *processState {
	t.Helper()
	limits := DefaultLimits()
	limits.Budget.Signals = NewQuota(100000)
	limits.MaxPendingSignals = 100000
	deployment := engineTestDeployment(t, newEngineTestDefinition(t, "engine.effect", "effect"), &engineTestDispatcher{})
	input, err := EncodePayload(engineTestInput{Value: "admission"})
	if err != nil {
		t.Fatal(err)
	}
	execution, state, _, err := initializeExecution(t.Context(), deployment.Definition(), input)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	id := newProcessID()
	handle := newProcessHandle(rootProcessRelation(id), deployment.DeploymentRef(), limits.Budget, CapabilitySet{}, DefaultTreeLimits(), now)
	process := newProcessState(handle, deployment, execution, state, now, limits)
	for index := range history {
		signal := mustMailboxSignal(t, fmt.Sprintf("signal:%d", index), WaitID{}, json.RawMessage(`{}`))
		if accepted, err := process.mailbox.enqueue(StatusRunning, signal, signalSourceExternal); err != nil || !accepted {
			t.Fatalf("history admission = %t, %v", accepted, err)
		}
	}
	if _, err := process.mailbox.commit(uint32(history)); err != nil {
		t.Fatal(err)
	}
	return process
}

func TestSignalAdmissionRejectsWholeBatchWithoutChangingHistoryOrWaits(t *testing.T) {
	for _, test := range []struct {
		name    string
		prepare func(*testing.T, *processState) []Signal
		want    error
	}{
		{name: "duplicate within batch", want: ErrSignalConflict, prepare: func(t *testing.T, _ *processState) []Signal {
			signal := mustMailboxSignal(t, "signal:new", WaitID{}, json.RawMessage(`{}`))
			return []Signal{signal, signal}
		}},
		{name: "conflict after duplicate", want: ErrSignalConflict, prepare: func(t *testing.T, _ *processState) []Signal {
			return []Signal{mustMailboxSignal(t, "signal:0", WaitID{}, json.RawMessage(`{}`)), mustMailboxSignal(t, "signal:new", WaitID{}, json.RawMessage(`{}`)), mustMailboxSignal(t, "signal:new", WaitID{}, json.RawMessage(`{"changed":true}`))}
		}},
		{name: "invalid after duplicate", want: ErrSignalRejected, prepare: func(t *testing.T, _ *processState) []Signal {
			return []Signal{mustMailboxSignal(t, "signal:0", WaitID{}, json.RawMessage(`{}`)), {}}
		}},
		{name: "unauthorized address after duplicate", want: ErrSignalRejected, prepare: func(t *testing.T, _ *processState) []Signal {
			wait, _ := ParseWaitID("wait:unknown")
			return []Signal{mustMailboxSignal(t, "signal:0", WaitID{}, json.RawMessage(`{}`)), mustMailboxSignal(t, "signal:new", wait, json.RawMessage(`{}`))}
		}},
		{name: "two answers to same wait", want: ErrSignalRejected, prepare: func(t *testing.T, process *processState) []Signal {
			wait := admissionTestWait(t, process)
			return []Signal{mustMailboxSignal(t, "signal:first", wait, json.RawMessage(`{}`)), mustMailboxSignal(t, "signal:second", wait, json.RawMessage(`{}`))}
		}},
		{name: "answer exceeds budget", want: ErrResourceLimitExceeded, prepare: func(t *testing.T, process *processState) []Signal {
			wait := admissionTestWait(t, process)
			process.limits.Budget.Signals = NewQuota(process.usage().AcceptedSignals)
			return []Signal{mustMailboxSignal(t, "signal:answer", wait, json.RawMessage(`{}`))}
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			process := admissionTestProcess(t, 10)
			signals := test.prepare(t, process)
			before := *process
			before.mailbox = process.mailbox.clone()
			accepted, err := admitTestSignals(process, signals, signalSourceExternal)
			if accepted || !errors.Is(err, test.want) {
				t.Fatalf("admission = %t, %v; want false, %v", accepted, err, test.want)
			}
			if !reflect.DeepEqual(before.mailbox, process.mailbox) || before.status != process.status || before.currentWaitID != process.currentWaitID || before.usage() != process.usage() {
				t.Fatal("rejected batch changed Process state")
			}
		})
	}
}

func TestSignalAdmissionAppliesWaitAnswerAndFollowingSignalTogether(t *testing.T) {
	process := admissionTestProcess(t, 10)
	wait := admissionTestWait(t, process)
	answer := mustMailboxSignal(t, "signal:answer", wait, json.RawMessage(`{"approved":true}`))
	steer := mustMailboxSignal(t, "signal:steer", WaitID{}, json.RawMessage(`{"next":"continue"}`))
	if accepted, err := admitTestSignals(process, []Signal{answer, steer}, signalSourceExternal); err != nil || !accepted {
		t.Fatalf("admission = %t, %v", accepted, err)
	}
	if process.status != StatusRunning || process.currentWaitID.Valid() || !process.mailbox.waits[wait].answered || process.usage().AcceptedSignals != 13 {
		t.Fatalf("batch did not atomically resume and charge the Process: status=%s usage=%+v", process.status, process.usage())
	}
	if got := process.mailbox.records[11:]; len(got) != 2 || got[0].id != answer.ID() || got[1].id != steer.ID() {
		t.Fatal("accepted batch lost arrival order")
	}
	restored := restoredMailbox(t, process.mailbox, process.status)
	process.mailbox = restored
	before := *process
	before.mailbox = process.mailbox.clone()
	if accepted, err := admitTestSignals(process, []Signal{answer, steer}, signalSourceExternal); err != nil || accepted {
		t.Fatalf("replay after restore = %t, %v", accepted, err)
	}
	if !reflect.DeepEqual(before.mailbox, process.mailbox) || before.status != process.status || before.currentWaitID != process.currentWaitID || before.usage() != process.usage() {
		t.Fatal("restored replay changed Process state")
	}
}

func admissionTestWait(t *testing.T, process *processState) WaitID {
	t.Helper()
	wait, _ := ParseWaitID("wait:approval")
	key, _ := ParseWaitKey("approval")
	signal := mustMailboxSignal(t, "signal:engine:opened", wait, json.RawMessage(`{}`))
	if err := process.mailbox.openWait(key, signal, WaitKindExternal); err != nil {
		t.Fatal(err)
	}
	process.status, process.currentWaitID = StatusWaiting, wait
	return wait
}

func BenchmarkMailboxCandidateCommittedHistory(b *testing.B) {
	for _, history := range []int{1_000, 10_000, 100_000} {
		b.Run(fmt.Sprint(history), func(b *testing.B) {
			process := admissionTestProcess(b, history)
			b.ReportAllocs()
			b.ResetTimer()
			for b.Loop() {
				candidate := process.candidate()
				if candidate.mailbox.pendingCount() != 0 || len(candidate.mailbox.records) != history {
					b.Fatal("candidate lost committed signal history")
				}
			}
		})
	}
}
