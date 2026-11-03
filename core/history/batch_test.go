package history_test

import (
	"bytes"
	"crypto/rand"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/history"
	"github.com/Tangerg/scope/core/metadata"
)

func TestBatchAdmitsOnlyCompleteIdentifiedAppends(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("question"))
	id, identifierErr := history.NewAppendID("append")
	if identifierErr != nil || id.String() != "append" {
		t.Fatal(id, identifierErr)
	}
	for _, id := range []string{"", " leading", "trailing ", "line\nbreak", string([]byte{0xff})} {
		if _, err := history.NewAppendID(id); !errors.Is(err, history.ErrInvalidAppendID) {
			t.Errorf("NewAppendID(%q) = %v", id, err)
		}
	}
	for _, test := range []struct {
		name         string
		conversation history.ConversationID
		id           history.AppendID
		messages     []chat.Message
	}{
		{"conversation", "", "append", []chat.Message{message}},
		{"append identity", "conversation", "", []chat.Message{message}},
		{"empty", "conversation", "append", nil},
		{"invalid message", "conversation", "append", []chat.Message{{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := history.NewBatch(test.conversation, test.id, test.messages...); !errors.Is(err, history.ErrInvalidBatch) {
				t.Fatal(err)
			}
		})
	}
	if err := (history.Batch{}).Validate(); !errors.Is(err, history.ErrInvalidBatch) {
		t.Fatal(err)
	}
	if _, err := jsonv2.Marshal(history.Batch{}); !errors.Is(err, history.ErrInvalidBatch) {
		t.Fatal(err)
	}
	var absent *history.Batch
	if err := absent.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, history.ErrInvalidBatch) {
		t.Fatal(err)
	}
}

func TestBatchCanonicalizesRawObjectsWithoutRoundingNumbers(t *testing.T) {
	left := chat.NewUserMessage(chat.NewTextPart("question"))
	left.Metadata = metadata.Map{"nested": json.RawMessage(` { "z": 9007199254740993, "a": {"d": 4, "c": 3} } `)}
	right := left.Clone()
	right.Metadata["nested"] = json.RawMessage(`{"a":{"c":3,"d":4},"z":9007199254740993}`)
	first := mustBatch(t, "conversation", "append", left)
	second := mustBatch(t, "conversation", "append", right)
	if first.Digest() != second.Digest() || !bytes.Equal(first.EncodedMessages(), second.EncodedMessages()) {
		t.Fatalf("equivalent object representations conflict:\n%s\n%s", first.EncodedMessages(), second.EncodedMessages())
	}
	if !bytes.Contains(first.EncodedMessages(), []byte("9007199254740993")) {
		t.Fatalf("exact integer was rounded: %s", first.EncodedMessages())
	}
	if !reflect.DeepEqual(first.Messages(), second.Messages()) {
		t.Fatal("message projections differ from their canonical wire owner")
	}
}

func TestBatchPreservesToolArgumentText(t *testing.T) {
	message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
		ID: "call", Name: "lookup", Arguments: `{"b":2,"a":1}`,
	}))
	first := mustBatch(t, "conversation", "append", message)
	message.Parts[0].ToolCall.Arguments = ` { "a": 1, "b": 2 } `
	second := mustBatch(t, "conversation", "append", message)
	if first.Digest() == second.Digest() {
		t.Fatal("history discarded the model's distinct argument text")
	}
	message.Parts[0].ToolCall.Arguments = `{"incomplete":`
	malformed := mustBatch(t, "conversation", "malformed", message)
	if got := malformed.Messages()[0].Parts[0].ToolCall.Arguments; got != message.Parts[0].ToolCall.Arguments {
		t.Fatalf("untrusted proposal changed: %q", got)
	}
}

func TestBatchOwnsItsSnapshotAndSurvivesSerialization(t *testing.T) {
	message := chat.NewUserMessage(chat.NewTextPart("question"))
	message.Metadata = metadata.Map{"source": json.RawMessage(`{"name":"original"}`)}
	batch := mustBatch(t, "conversation", "append", message)
	if batch.Len() != 1 {
		t.Fatalf("batch length = %d", batch.Len())
	}
	want := batch.Messages()
	digest := batch.Digest()
	message.Parts[0].Text = "mutated input"
	message.Metadata["source"][0] = 'x'
	messages := batch.Messages()
	messages[0].Parts[0].Text = "mutated projection"
	messages[0].Metadata["source"][0] = 'x'
	encoded := batch.EncodedMessages()
	encoded[0] = 'x'
	if batch.Digest() != digest || !reflect.DeepEqual(batch.Messages(), want) {
		t.Fatal("batch shares mutable caller memory")
	}
	wire, err := jsonv2.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	var recovered history.Batch
	if err := jsonv2.Unmarshal(wire, &recovered); err != nil {
		t.Fatal(err)
	}
	if recovered.ID() != batch.ID() || recovered.ConversationID() != batch.ConversationID() || recovered.Digest() != batch.Digest() || !reflect.DeepEqual(recovered.Messages(), want) {
		t.Fatal("serialized batch lost retry identity or content")
	}
	for _, malformed := range []string{
		`null`, `{}`, `{"conversation_id":"conversation","id":"append","messages":[]}`,
		string(wire[:len(wire)-1]) + `,"digest":"caller-owned"}`,
	} {
		if err := jsonv2.Unmarshal([]byte(malformed), &recovered); !errors.Is(err, history.ErrInvalidBatch) {
			t.Fatalf("invalid wire accepted: %s: %v", malformed, err)
		}
		if recovered.Digest() != batch.Digest() {
			t.Fatal("failed admission replaced the prior batch")
		}
	}
}

func TestReceiptConfirmsOnlyItsExactBatch(t *testing.T) {
	batch := mustBatch(t, "conversation", "append", chat.NewUserMessage(chat.NewTextPart("question")))
	receipt := history.Receipt{ConversationID: batch.ConversationID(), ID: batch.ID(), Digest: batch.Digest()}
	if err := receipt.ValidateFor(batch); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []history.Receipt{
		{},
		{ConversationID: "another", ID: receipt.ID, Digest: receipt.Digest},
		{ConversationID: receipt.ConversationID, ID: "another", Digest: receipt.Digest},
		{ConversationID: receipt.ConversationID, ID: receipt.ID, Digest: "another"},
	} {
		if err := candidate.ValidateFor(batch); !errors.Is(err, history.ErrInvalidReceipt) {
			t.Fatalf("receipt %+v accepted: %v", candidate, err)
		}
	}
	if err := receipt.ValidateFor(history.Batch{}); !errors.Is(err, history.ErrInvalidReceipt) {
		t.Fatal(err)
	}
}

func mustBatch(t testing.TB, conversation history.ConversationID, id history.AppendID, messages ...chat.Message) history.Batch {
	t.Helper()
	batch, err := history.NewBatch(conversation, id, messages...)
	if err != nil {
		t.Fatal(err)
	}
	return batch
}

func appendMessages(t testing.TB, writer history.Writer, conversation history.ConversationID, messages ...chat.Message) (history.Receipt, error) {
	t.Helper()
	return writer.Append(t.Context(), mustBatch(t, conversation, history.AppendID(rand.Text()), messages...))
}
