package agent

import (
	"context"
	"encoding/json"
	"testing"
)

type quotaLoopDefinition struct{ descriptor Descriptor }

func (q quotaLoopDefinition) Descriptor() Descriptor { return q.descriptor }

func (q quotaLoopDefinition) Start(input Payload) (Execution, error) {
	remaining, err := input.Decode[uint64]()
	if err != nil {
		return nil, err
	}
	return &quotaLoopExecution{remaining: remaining}, nil
}

func (q quotaLoopDefinition) Restore(ctx context.Context, state ExecutionState) (Execution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	remaining, err := state.Decode[uint64]("quota_loop")
	if err != nil {
		return nil, err
	}
	return &quotaLoopExecution{remaining: remaining}, nil
}

type quotaLoopExecution struct{ remaining uint64 }

func (q *quotaLoopExecution) Step(ctx context.Context, _ []Signal) (Transition, error) {
	if err := ctx.Err(); err != nil {
		return Transition{}, err
	}
	if q.remaining == 0 {
		return Complete(0, controlValue(EncodePayload(uint64(0))))
	}
	q.remaining--
	return Continue(0)
}

func (q *quotaLoopExecution) Snapshot() (ExecutionState, error) {
	encoded, err := json.Marshal(q.remaining)
	if err != nil {
		return ExecutionState{}, err
	}
	return NewExecutionState("quota_loop", encoded)
}

func TestUnlimitedExecutionExceedsFormerDefaultSteps(t *testing.T) {
	schema := controlValue(SchemaFor[uint64]())
	definition := quotaLoopDefinition{descriptor: controlValue(NewDescriptor(DescriptorConfig{Name: "test.quota_loop", Description: "Continue for the requested number of Steps.", InputSchema: schema, OutputSchema: schema}))}
	deployment := controlValue(NewDeployment(DeploymentConfig{Definition: definition, ImplementationDigest: ComputeDigest([]byte("quota-loop")), ConfigurationDigest: ComputeDigest([]byte("unlimited"))}))
	engine := controlValue(NewEngine(EngineConfig{}))
	defer mustCloseEngine(t, engine)
	result, err := engine.Run(t.Context(), deployment, controlValue(EncodePayload(uint64(10001))))
	if err != nil || result.Status() != StatusCompleted || result.Usage().CommittedSteps != 10002 {
		t.Fatalf("result=%s steps=%d error=%v", result.Status(), result.Usage().CommittedSteps, err)
	}
}

func TestUnlimitedTreeQuotasDoNotDisableConcurrency(t *testing.T) {
	runtime := newWaitingSnapshotTree(t, 2)
	root := runtime.processes[runtime.rootID]
	root.treeLimits.MaxChildren = Quota{}
	root.treeLimits.MaxTreeProcesses = Quota{}
	root.treeLimits.MaxActiveChildren = 1
	if runtime.canStartChild(root) {
		t.Fatal("active child capacity was disabled")
	}
	child := runtime.processes[runtime.childrenByParent[root.handle.processID][0]]
	child.status = StatusCompleted
	if !runtime.canStartChild(root) {
		t.Fatal("completed child still consumed concurrent capacity")
	}
}
