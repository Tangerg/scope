package managed

import (
	"encoding/json/jsontext"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestOptionalWaitFieldsProjectWithoutChangingTheirContract(t *testing.T) {
	root, _ := run.ParseProcessID("root")
	key, _ := run.ParseWaitKey("answer")
	question, _ := run.EncodePayload("Question")
	schema, _ := jsonschema.For[string]()
	for _, encoding := range []struct {
		name  string
		modes []string
	}{
		{"text", []string{a2a.MediaTypePlainText}},
		{"JSON", []string{a2a.MediaTypeJSON}},
		{"mixed", []string{a2a.MediaTypePlainText, a2a.MediaTypeJSON}},
	} {
		for _, fields := range []struct {
			name     string
			question run.Payload
			schema   jsonschema.Schema
		}{
			{"present", question, schema},
			{"absent-schema", question, jsonschema.Schema{}},
			{"absent-question", run.Payload{}, schema},
			{"absent-both", run.Payload{}, jsonschema.Schema{}},
		} {
			t.Run(encoding.name+"/"+fields.name, func(t *testing.T) {
				wait := run.Wait{Key: key, Question: fields.question, AnswerSchema: fields.schema}
				if !wait.Valid() {
					t.Fatal("invalid fixture")
				}
				policy, err := newContentPolicy(sdk.AgentCard{DefaultOutputModes: encoding.modes})
				if err != nil {
					t.Fatal(err)
				}
				head := run.TreeInspection{Root: root, Processes: []run.ProcessInspection{{ID: root, Status: run.StatusWaiting, Waits: []run.Wait{wait}}}}
				task, err := projectTask(taskKey{id: "task", context: "context"}, head, policy)
				if err != nil || task.Status.State != sdk.TaskStateInputRequired || task.Status.Message == nil {
					t.Fatalf("task=%+v error=%v", task, err)
				}
				message := task.Status.Message
				rawSchema, _ := message.Metadata[a2a.AnswerSchemaMetadataKey].(string)
				wantSchema := "true"
				if fields.schema.Valid() {
					wantSchema = string(fields.schema.JSON())
				}
				if rawSchema != wantSchema {
					t.Fatalf("answer schema=%s, want %s", rawSchema, wantSchema)
				}
				part := message.Parts[0]
				wantQuestion := "null"
				if fields.question.Valid() {
					wantQuestion = `"Question"`
					if part.MediaType == a2a.MediaTypePlainText {
						wantQuestion = "Question"
					}
				}
				if part.Text() != wantQuestion || part.MediaType == a2a.MediaTypeJSON && !jsontext.Value(part.Text()).IsValid() {
					t.Fatalf("question=%q (%s), want %q", part.Text(), part.MediaType, wantQuestion)
				}
				pending := task.Metadata[PendingKey].([]pendingQuestion)[0]
				if string(pending.AnswerSchema) != rawSchema || !jsontext.Value(pending.Question).IsValid() {
					t.Fatalf("pending metadata disagrees with the question: %+v", pending)
				}
			})
		}
	}
}

func TestQuestionIdentityUsesTheRuntimeRootAndWaitAddress(t *testing.T) {
	message := &sdk.Message{ID: "same-message", ContextID: "same-context"}
	alice, err := taskKeyFromMessage("alice", message)
	if err != nil {
		t.Fatal(err)
	}
	bob, err := taskKeyFromMessage("bob", message)
	if err != nil || alice.id != bob.id || alice.start.Root() == bob.start.Root() {
		t.Fatalf("fixture does not exercise authority namespaces: alice=%+v bob=%+v error=%v", alice, bob, err)
	}
	answer, _ := run.ParseWaitKey("answer")
	otherAnswer, _ := run.ParseWaitKey("other-answer")
	child, _ := run.ParseProcessID(alice.start.Root().String() + "/child")
	policy, _ := newContentPolicy(sdk.AgentCard{})
	project := func(key taskKey, owner run.ProcessID, wait run.WaitKey, head run.LSN, epoch run.Epoch) string {
		t.Helper()
		processes := []run.ProcessInspection{{ID: key.start.Root(), Status: run.StatusWaiting}}
		waiting := run.ProcessInspection{ID: owner, Status: run.StatusWaiting, Waits: []run.Wait{{Key: wait}}}
		if owner == key.start.Root() {
			processes[0] = waiting
		} else {
			processes = append(processes, waiting)
		}
		task, projectErr := projectTask(key, run.TreeInspection{Root: key.start.Root(), Head: head, Epoch: epoch, Processes: processes}, policy)
		if projectErr != nil {
			t.Fatal(projectErr)
		}
		return task.Status.Message.ID
	}
	first := project(alice, alice.start.Root(), answer, 5, 1)
	if first == "" || first != project(alice, alice.start.Root(), answer, 5, 1) || first != project(alice, alice.start.Root(), answer, 20, 2) {
		t.Fatal("one committed wait changed identity across reads, progress, or recovery")
	}
	seen := map[string]bool{first: true}
	for _, id := range []string{
		project(bob, bob.start.Root(), answer, 5, 1),
		project(alice, child, answer, 5, 1),
		project(alice, alice.start.Root(), otherAnswer, 5, 1),
	} {
		if seen[id] {
			t.Fatal("distinct runtime root or wait address reused a question identity")
		}
		seen[id] = true
	}
}
