package managed

import (
	"encoding/json"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
)

// Task metadata keys a managed task publishes; clients read each by name.
const (
	// PendingKey lists the questions the tree is waiting on.
	PendingKey = "scope.pending"
	// ProgramKey names the Program reference the task runs.
	ProgramKey = "scope.program"
	// RootKey is the root Process ID.
	RootKey = "scope.root_id"
	// RootStatusKey is the root Process status.
	RootStatusKey = "scope.root_status"
	// TerminationKey is the root's termination cause once it has exited.
	TerminationKey = "scope.termination"
)

const (
	resultArtifactID              = "result"
	questionMessageIdentityDomain = "a2a.wait.question"
	absentQuestionJSON            = "null"
	unrestrictedAnswerSchemaJSON  = "true"
)

type pendingQuestion struct {
	Address        a2a.WaitAddress `json:"address"`
	Question       json.RawMessage `json:"question"`
	AnswerSchema   json.RawMessage `json:"answer_schema"`
	MaxAnswerBytes int             `json:"max_answer_bytes"`
}

func newPendingQuestion(owner run.ProcessID, wait run.Wait) pendingQuestion {
	question := pendingQuestion{
		Address:  a2a.WaitAddress{Process: owner.String(), Key: wait.Key.String()},
		Question: wait.Question.JSON(), AnswerSchema: wait.AnswerSchema.JSON(), MaxAnswerBytes: wait.MaxAnswerBytes,
	}
	if !wait.Question.Valid() {
		question.Question = json.RawMessage(absentQuestionJSON)
	}
	if !wait.AnswerSchema.Valid() {
		question.AnswerSchema = json.RawMessage(unrestrictedAnswerSchemaJSON)
	}
	return question
}

func (p pendingQuestion) message(root run.ProcessID, task *sdk.Task, content contentPolicy) (*sdk.Message, error) {
	address, err := p.Address.Metadata()
	if err != nil {
		return nil, err
	}
	id, err := identityDigest(questionMessageIdentityDomain, root.String(), p.Address.Process, p.Address.Key)
	if err != nil {
		return nil, err
	}
	return &sdk.Message{
		ID: id, Role: sdk.MessageRoleAgent, TaskID: task.ID, ContextID: task.ContextID,
		Parts:    []*sdk.Part{content.part(p.Question)},
		Metadata: map[string]any{a2a.WaitMetadataKey: address, a2a.AnswerSchemaMetadataKey: string(p.AnswerSchema)},
	}, nil
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
		RootKey: head.Root.String(), ProgramKey: root.Program.String(), RootStatusKey: root.Status.String(),
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
		task.Metadata[TerminationKey] = root.Termination.Cause().String()
		return task, nil
	}
	var questions []pendingQuestion
	for _, process := range head.Processes {
		for _, wait := range process.Waits {
			questions = append(questions, newPendingQuestion(process.ID, wait))
		}
	}
	if len(questions) > 0 {
		message, err := questions[0].message(head.Root, task, content)
		if err != nil {
			return nil, err
		}
		task.Metadata[PendingKey] = questions
		task.Status.State = sdk.TaskStateInputRequired
		task.Status.Message = message
	}
	return task, nil
}
