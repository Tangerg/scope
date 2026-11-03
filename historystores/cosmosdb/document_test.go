package cosmosdb

import (
	"math"
	"strings"
	"testing"
)

func TestDocumentSequenceIsExactAndBounded(t *testing.T) {
	if got, want := formatSequence(42), "0000000000000000042"; got != want {
		t.Fatalf("sequence=%q want=%q", got, want)
	}
	for _, sequence := range []string{"", "42", "000000000000000004x", "-000000000000000042"} {
		if validSequence(sequence) {
			t.Fatalf("accepted %q", sequence)
		}
	}
	if !validSequence(formatSequence(math.MaxInt64)) {
		t.Fatal("maximum sequence lost precision")
	}
	conversation := newConversationDocument("conversation")
	if err := conversation.validate("conversation"); err != nil {
		t.Fatal(err)
	}
	advanced, err := conversation.advance()
	if err != nil || advanced.LastSequence != formatSequence(1) || conversation.LastSequence != formatSequence(0) {
		t.Fatalf("advance=%+v error=%v", advanced, err)
	}
	conversation.LastSequence = formatSequence(math.MaxInt64)
	if _, err := conversation.advance(); err == nil {
		t.Fatal("sequence overflow accepted")
	}
	conversation.LastSequence = "invalid"
	if _, err := conversation.advance(); err == nil {
		t.Fatal("invalid sequence accepted")
	}
	closed := newConversationDocument("conversation").close()
	if !closed.closed() {
		t.Fatal("closure missing")
	}
	closed.Closed = nil
	if err := closed.validate("conversation"); err == nil {
		t.Fatal("absent closure accepted")
	}
}

func TestStoredDocumentRequiresPermanentIdentity(t *testing.T) {
	id, err := appendDocumentID("append")
	if err != nil {
		t.Fatal(err)
	}
	valid := appendDocument{ID: id, Kind: appendKind, ConversationID: "conversation", AppendID: "append", Digest: "opaque", Sequence: formatSequence(1), Messages: `[{"role":"user"}]`, TTL: permanentTTL}
	if err := valid.validate("conversation"); err != nil {
		t.Fatal(err)
	}
	for _, change := range []func(*appendDocument){
		func(d *appendDocument) { d.ID = "other" }, func(d *appendDocument) { d.AppendID = "" }, func(d *appendDocument) { d.Digest = "" }, func(d *appendDocument) { d.TTL = 1 }, func(d *appendDocument) { d.Sequence = formatSequence(0) }, func(d *appendDocument) { d.AppendID = strings.Repeat("a", 2000) },
	} {
		copy := valid
		change(&copy)
		if err := copy.validate("conversation"); err == nil {
			t.Fatalf("invalid document accepted: %+v", copy)
		}
	}
	if _, err := decodeMessages([]byte(`[{}]`)); err == nil {
		t.Fatal("invalid message accepted")
	}
	if _, err := decodeMessages([]byte(`[]`)); err == nil {
		t.Fatal("empty append accepted")
	}
}
