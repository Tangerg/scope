package managed

import (
	"errors"
	"strings"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"

	"github.com/Tangerg/scope/agent/run"
)

func TestTaskKeyBindsContextAndAuthority(t *testing.T) {
	message := &sdk.Message{ID: "message", ContextID: "context"}
	key, err := taskKeyFromMessage("a2a:scope", message)
	if err != nil {
		t.Fatal(err)
	}
	want := taskKey{
		id:      "Y29udGV4dA.2bbcb15869fcc6de43b95fca8d5bdb55aa3e3154d7420b9e4ec769141dd3ccdb",
		context: "context",
		start:   run.StartID{Scope: "a2a:scope", Key: "24aab35e4b554bbe9b6d41e3d8b993dace9d4a6e0ff4efaaf9b835a143fa9d2f"},
	}
	if key != want {
		t.Fatalf("key=%+v, want %+v", key, want)
	}
	other, err := parseTaskKey("a2a:other", key.id)
	if err != nil || other.id != key.id || other.context != key.context || other.start != (run.StartID{Scope: "a2a:other", Key: key.start.Key}) {
		t.Fatalf("task token widened its authority: %+v %v", other, err)
	}
	message.TaskID, message.ID = key.id, "reply"
	if followup, err := taskKeyFromMessage("a2a:scope", message); err != nil || followup != key {
		t.Fatalf("follow-up changed task identity: %+v %v", followup, err)
	}
	message.ContextID = "different"
	if _, err := taskKeyFromMessage("a2a:scope", message); !errors.Is(err, sdk.ErrInvalidParams) {
		t.Fatalf("mismatching context accepted: %v", err)
	}
}

func TestTaskIdentityBoundsPrecedeTokenCreation(t *testing.T) {
	maximum := &sdk.Message{ID: strings.Repeat("m", 1024), ContextID: strings.Repeat("c", 1024)}
	key, err := taskKeyFromMessage("a2a:scope", maximum)
	if err != nil {
		t.Fatal(err)
	}
	if decoded, err := parseTaskKey("a2a:scope", key.id); err != nil || decoded != key {
		t.Fatalf("maximum identity did not round trip: %+v %v", decoded, err)
	}
	for _, message := range []*sdk.Message{
		nil,
		{ID: " "},
		{ID: strings.Repeat("m", 1025)},
		{ID: "message", ContextID: strings.Repeat("c", 1025)},
		{ID: string([]byte{0xff})},
		{ID: "message", ContextID: string([]byte{0xff})},
		{ID: strings.Repeat("m", 1025), TaskID: key.id},
	} {
		if _, err := taskKeyFromMessage("a2a:scope", message); !errors.Is(err, sdk.ErrInvalidParams) {
			t.Fatalf("invalid message identity=%+v error=%v", message, err)
		}
	}
	if _, err := parseTaskKey("a2a:scope", sdk.TaskID(strings.Repeat("x", 1501))); !errors.Is(err, sdk.ErrTaskNotFound) {
		t.Fatalf("oversized task token accepted: %v", err)
	}
}
