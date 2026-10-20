package history_test

import (
	"errors"
	"testing"

	"github.com/Tangerg/scope/core/history"
)

func TestConversationIDValidation(t *testing.T) {
	for _, id := range []string{"", " padded", "padded ", "\tvalue", "value\x00", string([]byte{0xff})} {
		if _, err := history.NewConversationID(id); !errors.Is(err, history.ErrInvalidConversationID) {
			t.Fatalf("NewConversationID(%q) = %v", id, err)
		}
	}
	conversationID, err := history.NewConversationID("opaque/id:1")
	if err != nil {
		t.Fatal(err)
	}
	if conversationID.String() != "opaque/id:1" {
		t.Fatalf("ConversationID.String() = %q", conversationID.String())
	}
}
