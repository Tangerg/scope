package managed

import (
	"context"
	"errors"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/a2a"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestJSONContentDoesNotRoundAnAgentAnswer(t *testing.T) {
	raw := sdk.NewRawPart([]byte(`{"amount":9007199254740993}`))
	raw.MediaType = "application/json"
	for _, part := range []*sdk.Part{raw, sdk.NewDataPart(map[string]any{"enabled": true}), sdk.NewDataPart(map[string]any{"risk": float64(9007199254740992)})} {
		out, err := parseMessageContent(sdk.NewMessage(sdk.MessageRoleUser, part))
		if v, ok := part.Content.(sdk.Data); ok {
			if _, has := v.Value.(map[string]any)["risk"]; has {
				if !errors.Is(err, sdk.ErrUnsupportedContentType) {
					t.Fatal("rounded SDK value accepted", err)
				}
				continue
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if part == raw && string(out.payload.JSON()) != `{"amount":9007199254740993}` {
			t.Fatalf("lost integer: %s", out.payload.JSON())
		}
	}
}

func TestInvalidHostAuthorityCannotCollapseTwoNamespaces(t *testing.T) {
	h := &handler{service: "book", authority: func(_ context.Context, tenant string) (string, error) { return string([]byte{0xff}) + tenant, nil }}
	a, ea := h.scope(t.Context(), "alice")
	b, eb := h.scope(t.Context(), "bob")
	if ea == nil || eb == nil {
		t.Fatalf("invalid authorities granted task namespaces: alice=%q %v bob=%q %v", a, ea, b, eb)
	}
}

func TestContentPolicyRequiresEveryPossibleOutputMode(t *testing.T) {
	card := sdk.AgentCard{DefaultInputModes: []string{"application/json"}, DefaultOutputModes: []string{"text/plain", "application/json"}}
	policy, err := newContentPolicy(card)
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		accepted []string
		valid    bool
	}{
		{accepted: []string{"text/plain"}},
		{accepted: []string{"application/json"}},
		{accepted: []string{"text/plain", "application/json"}, valid: true},
		{valid: true},
	} {
		request := &sdk.SendMessageRequest{
			Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewDataPart(map[string]any{"enabled": true})),
			Config:  &sdk.SendMessageConfig{AcceptedOutputModes: sample.accepted},
		}
		payload, prepareErr := policy.prepare(request)
		if sample.valid {
			if prepareErr != nil || string(payload.JSON()) != `{"enabled":true}` {
				t.Fatalf("accepted=%v payload=%s error=%v", sample.accepted, payload.JSON(), prepareErr)
			}
		} else if !errors.Is(prepareErr, sdk.ErrUnsupportedContentType) {
			t.Fatalf("accepted=%v error=%v", sample.accepted, prepareErr)
		}
	}
	card.DefaultInputModes[0] = "text/plain"
	card.DefaultOutputModes[0] = "image/png"
	card.DefaultOutputModes[1] = "image/jpeg"
	_, err = policy.prepare(&sdk.SendMessageRequest{Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart("hello"))})
	if !errors.Is(err, sdk.ErrUnsupportedContentType) {
		t.Fatalf("mutating the host card changed the bound input policy: %v", err)
	}
	_, err = policy.prepare(&sdk.SendMessageRequest{
		Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewDataPart(map[string]any{"enabled": true})),
		Config:  &sdk.SendMessageConfig{AcceptedOutputModes: []string{"text/plain", "application/json"}},
	})
	if err != nil || policy.part([]byte(`"frozen"`)).MediaType != mediaTypePlainText {
		t.Fatalf("mutating the host card changed the bound output policy: %v", err)
	}
}

func TestContentPolicyRejectsUnservedConfiguration(t *testing.T) {
	policy, err := newContentPolicy(sdk.AgentCard{})
	if err != nil {
		t.Fatal(err)
	}
	for _, sample := range []struct {
		config sdk.SendMessageConfig
		want   error
	}{
		{config: sdk.SendMessageConfig{HistoryLength: new(-1)}, want: sdk.ErrInvalidParams},
		{config: sdk.SendMessageConfig{HistoryLength: new(1)}, want: sdk.ErrUnsupportedOperation},
		{config: sdk.SendMessageConfig{PushConfig: &sdk.PushConfig{}}, want: sdk.ErrPushNotificationNotSupported},
	} {
		_, err := policy.prepare(&sdk.SendMessageRequest{Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart("hello")), Config: &sample.config})
		if !errors.Is(err, sample.want) {
			t.Fatalf("configuration=%+v error=%v, want %v", sample.config, err, sample.want)
		}
	}
}

func TestProjectedPartsPreserveExactJSONAndStringMeaning(t *testing.T) {
	for _, sample := range []struct {
		raw, text string
	}{
		{raw: `"hello"`, text: "hello"},
		{raw: `null`, text: "null"},
		{raw: `{"amount":9007199254740993}`, text: `{"amount":9007199254740993}`},
		{raw: `18446744073709551615`, text: `18446744073709551615`},
	} {
		for _, modes := range [][]string{nil, {mediaTypePlainText}, {mediaTypeJSON}, {mediaTypePlainText, mediaTypeJSON}} {
			policy, err := newContentPolicy(sdk.AgentCard{DefaultOutputModes: modes})
			if err != nil {
				t.Fatal(err)
			}
			part := policy.part([]byte(sample.raw))
			wantMode, wantText := mediaTypeJSON, sample.raw
			if len(modes) != 0 && modes[0] == mediaTypePlainText {
				wantText = sample.text
				if len(modes) == 1 || sample.raw == `"hello"` {
					wantMode = mediaTypePlainText
				}
			}
			if part.MediaType != wantMode || part.Text() != wantText {
				t.Fatalf("modes=%v raw=%s part=%+v, want %s %q", modes, sample.raw, part, wantMode, wantText)
			}
		}
	}
}

func TestQuestionsUseTheSameDeclaredOutputPolicyAsFinalArtifacts(t *testing.T) {
	root, err := run.ParseProcessID("question-modes")
	if err != nil {
		t.Fatal(err)
	}
	key, err := run.ParseWaitKey("confirm")
	if err != nil {
		t.Fatal(err)
	}
	answerSchema, err := jsonschema.For[bool]()
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, accepted, question, text string
	}{
		{"structured question in text", mediaTypePlainText, `{"confirm":"Proceed?","amount":9007199254740993}`, `{"amount":9007199254740993,"confirm":"Proceed?"}`},
		{"string question in JSON", mediaTypeJSON, `"Proceed?"`, `"Proceed?"`},
	} {
		t.Run(test.name, func(t *testing.T) {
			policy, err := newContentPolicy(sdk.AgentCard{DefaultOutputModes: []string{test.accepted}})
			if err != nil {
				t.Fatal(err)
			}
			request := &sdk.SendMessageRequest{Message: sdk.NewMessage(sdk.MessageRoleUser, sdk.NewTextPart("start")), Config: &sdk.SendMessageConfig{AcceptedOutputModes: []string{test.accepted}}}
			if _, err = policy.prepare(request); err != nil {
				t.Fatal(err)
			}
			question, err := run.ParsePayload([]byte(test.question))
			if err != nil {
				t.Fatal(err)
			}
			head := run.TreeInspection{Root: root, Processes: []run.ProcessInspection{{ID: root, Status: run.StatusWaiting, Waits: []run.Wait{{Key: key, Question: question, AnswerSchema: answerSchema}}}}}
			task, err := projectTask(taskKey{id: "task", context: "context"}, head, policy)
			if err != nil {
				t.Fatal(err)
			}
			message := task.Status.Message
			if message == nil || len(message.Parts) != 1 || message.Parts[0].MediaType != test.accepted || message.Parts[0].Text() != test.text {
				t.Fatalf("question escaped the declared output modes: %+v", message)
			}
			if message.Metadata[a2a.AnswerSchemaMetadataKey] != string(answerSchema.JSON()) || message.Metadata[a2a.WaitMetadataKey] == nil {
				t.Fatal("question encoding discarded its answer contract or address")
			}
			pending := task.Metadata[PendingKey].([]pendingQuestion)
			if len(pending) != 1 || string(pending[0].Question) != string(question.JSON()) || string(pending[0].AnswerSchema) != string(answerSchema.JSON()) {
				t.Fatalf("question metadata lost exact values: %+v", pending)
			}
		})
	}
}
