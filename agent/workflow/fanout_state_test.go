package workflow_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/workflow"
)

func TestFanoutRestoreRejectsInvalidWindowState(t *testing.T) {
	child := mustDeployment(t, mustDefinition(t, "test.workflow.window_child",
		mustTransform(t, "identity", func(_ context.Context, input forkInput) (numberOutput, error) {
			return numberOutput(input), nil
		}),
	), "window-child")
	stage, err := workflow.Map(workflow.MapConfig[forkInput, numberOutput]{
		ID: "items", Deployment: child, Budget: mustBudget(t), WindowSize: 2, MaxItems: 3,
	})
	if err != nil {
		t.Fatal(err)
	}
	definition := mustDefinition(t, "test.workflow.window", stage)
	input, err := agent.EncodeInput([]forkInput{{Value: 1}, {Value: 2}, {Value: 3}})
	if err != nil {
		t.Fatal(err)
	}
	execution, err := definition.Start(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, stepErr := execution.Step(t.Context(), nil); stepErr != nil {
		t.Fatal(stepErr)
	}
	snapshot, err := execution.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := definition.Restore(snapshot); err != nil {
		t.Fatalf("Restore() rejected the initial window: %v", err)
	}

	for _, test := range []struct {
		name      string
		fields    map[string]json.RawMessage
		wantValid bool
	}{
		{
			name: "final partial window",
			fields: map[string]json.RawMessage{
				"active_fanout_window":     json.RawMessage(`[{}]`),
				"completed_fanout_outputs": json.RawMessage(`[{"value":1},{"value":2}]`),
			},
			wantValid: true,
		},
		{
			name: "completed prefix contains invalid output",
			fields: map[string]json.RawMessage{
				"active_fanout_window":     json.RawMessage(`[{}]`),
				"completed_fanout_outputs": json.RawMessage(`[null,{"value":2}]`),
			},
		},
		{
			name: "undersized non-final window",
			fields: map[string]json.RawMessage{
				"active_fanout_window": json.RawMessage(`[{}]`),
			},
		},
		{
			name: "window starts between fixed boundaries",
			fields: map[string]json.RawMessage{
				"active_fanout_window":     json.RawMessage(`[{},{}]`),
				"completed_fanout_outputs": json.RawMessage(`[{"value":1}]`),
			},
		},
		{
			name: "completed outputs leave no active input",
			fields: map[string]json.RawMessage{
				"completed_fanout_outputs": json.RawMessage(`[{"value":1},{"value":2},{"value":3}]`),
			},
		},
		{
			name: "duplicate children before wait opens",
			fields: map[string]json.RawMessage{
				"phase":                json.RawMessage(`"awaiting_fanout_wait_open"`),
				"active_fanout_window": json.RawMessage(`[{"child_process_id":"child"},{"child_process_id":"child"}]`),
			},
		},
		{
			name: "duplicate children in active wait",
			fields: map[string]json.RawMessage{
				"phase":                json.RawMessage(`"waiting_fanout"`),
				"wait_id":              json.RawMessage(`"wait"`),
				"active_fanout_window": json.RawMessage(`[{"child_process_id":"child"},{"child_process_id":"child"}]`),
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(snapshot.Payload(), &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range test.fields {
				fields[key] = value
			}
			payload, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			state, err := agent.NewExecutionState(snapshot.Kind(), payload)
			if err != nil {
				t.Fatal(err)
			}
			_, restoreErr := definition.Restore(state)
			if test.wantValid {
				if restoreErr != nil {
					t.Fatalf("Restore() rejected a valid window: %v", restoreErr)
				}
			} else if !errors.Is(restoreErr, workflow.ErrInvalidExecutionState) {
				t.Fatalf("Restore() error = %v, want ErrInvalidExecutionState", restoreErr)
			}
		})
	}
}
