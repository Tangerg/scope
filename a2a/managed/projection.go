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
	Address        a2a.WaitAddress `json:"address"`
	Question       json.RawMessage `json:"question"`
	AnswerSchema   json.RawMessage `json:"answer_schema"`
	MaxAnswerBytes int             `json:"max_answer_bytes"`
}

func rootProcess(head run.TreeInspection) run.ProcessInspection {
	root, _ := head.Process(head.Root)
	return root
}

func projectTask(k taskKey, head run.TreeInspection, content contentPolicy) (*sdk.Task, error) {
	root := rootProcess(head)
	if !root.ID.Valid() {
		return nil, sdk.ErrInternalError
	}
	task := &sdk.Task{ID: k.id, ContextID: k.context, Status: sdk.TaskStatus{State: sdk.TaskStateWorking}, Metadata: map[string]any{
		RootKey: head.Root.String(), ProgramKey: root.Program.String(), rootStatusMetadataKey: root.Status.String(),
	}}
	if root.Termination.Valid() {
		switch root.Termination.Status() {
		case run.StatusCompleted:
			task.Status.State = sdk.TaskStateCompleted
			if payload, ok := root.Termination.Output(); ok {
				task.Artifacts = []*sdk.Artifact{{ID: resultArtifactID, Parts: []*sdk.Part{content.part(payload.JSON())}}}
			}
		case run.StatusCanceled, run.StatusKilled:
			task.Status.State = sdk.TaskStateCanceled
		default:
			task.Status.State = sdk.TaskStateFailed
		}
		task.Metadata[terminationMetadataKey] = root.Termination.Cause().String()
		return task, nil
	}
	var questions []pendingQuestion
	for _, process := range head.Processes {
		for _, wait := range process.Waits {
			questions = append(questions, pendingQuestion{
				Address:  a2a.WaitAddress{Process: process.ID.String(), Key: wait.Key.String()},
				Question: wait.Question.JSON(), AnswerSchema: wait.AnswerSchema.JSON(), MaxAnswerBytes: wait.MaxAnswerBytes,
			})
		}
	}
	if len(questions) > 0 {
		address, err := questions[0].Address.Metadata()
		if err != nil {
			return nil, err
		}
		task.Metadata[PendingKey] = questions
		task.Status.State = sdk.TaskStateInputRequired
		task.Status.Message = sdk.NewMessageForTask(sdk.MessageRoleAgent, task, content.part(questions[0].Question))
		task.Status.Message.Metadata = map[string]any{a2a.WaitMetadataKey: address, a2a.AnswerSchemaMetadataKey: string(questions[0].AnswerSchema)}
	}
	return task, nil
}
