package managed

import (
	"encoding/json"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
)

const (
	PendingKey             = "scope.pending"
	ProgramKey             = "scope.program"
	RootKey                = "scope.root_id"
	rootStatusMetadataKey  = "scope.root_status"
	terminationMetadataKey = "scope.termination"
	resultArtifactID       = "result"
)

type pendingQuestion struct {
	ProcessID      string          `json:"process_id"`
	WaitID         string          `json:"wait_id"`
	Key            string          `json:"key"`
	Question       json.RawMessage `json:"question"`
	AnswerSchema   json.RawMessage `json:"answer_schema"`
	MaxAnswerBytes int             `json:"max_answer_bytes"`
}

func rootProcess(head run.TreeSnapshot) run.ProcessSnapshot {
	for _, p := range head.ProcessSnapshots() {
		if p.Relation().IsRoot() {
			return p
		}
	}
	return run.ProcessSnapshot{}
}
func projectTask(k taskKey, head run.TreeSnapshot) (*sdk.Task, error) {
	root := rootProcess(head)
	if !root.Valid() {
		return nil, sdk.ErrInternalError
	}
	t := &sdk.Task{ID: k.id, ContextID: k.context, Status: sdk.TaskStatus{State: sdk.TaskStateWorking}, Metadata: map[string]any{RootKey: root.Relation().ProcessID().String(), ProgramKey: root.ProgramRef().String(), rootStatusMetadataKey: root.Status().String()}}
	for _, member := range head.ProcessSnapshots() {
		if root.Status().Terminal() && !member.Status().Terminal() {
			return t, nil
		}
	}
	if result, ok := root.Result(); ok {
		termination := result.Termination()
		switch termination.Status() {
		case run.StatusCompleted:
			t.Status.State = sdk.TaskStateCompleted
			if payload, ok := termination.Output(); ok {
				t.Artifacts = []*sdk.Artifact{{ID: resultArtifactID, Parts: []*sdk.Part{jsonPart(payload.JSON())}}}
			}
		case run.StatusCanceled, run.StatusKilled:
			t.Status.State = sdk.TaskStateCanceled
		default:
			t.Status.State = sdk.TaskStateFailed
		}
		t.Metadata[terminationMetadataKey] = termination.Cause().String()
		return t, nil
	}
	pending := head.Pending()
	if len(pending) > 0 {
		questions := make([]pendingQuestion, 0, len(pending))
		for _, w := range pending {
			questions = append(questions, pendingQuestion{ProcessID: w.ProcessID.String(), WaitID: w.WaitID.String(), Key: w.Key.String(), Question: w.Question, AnswerSchema: w.AnswerSchema, MaxAnswerBytes: w.MaxAnswerBytes})
		}
		t.Metadata[PendingKey] = questions
		t.Status.State = sdk.TaskStateInputRequired
		t.Status.Message = sdk.NewMessageForTask(sdk.MessageRoleAgent, t, jsonPart(pending[0].Question))
		t.Status.Message.Metadata = map[string]any{a2a.WaitIDMetadataKey: pending[0].WaitID.String(), a2a.AnswerSchemaMetadataKey: string(pending[0].AnswerSchema)}
	}
	return t, nil
}
