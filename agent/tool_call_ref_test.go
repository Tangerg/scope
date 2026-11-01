package agent_test

import (
	"encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent"
)

func TestToolCallRefCanonicalEncoding(t *testing.T) {
	for _, encoded := range []string{
		"process:parent/1/0",
		"process:parent/18446744073709551615/4294967295",
	} {
		reference, err := agent.ParseToolCallRef(encoded)
		if err != nil || !reference.Valid() || reference.String() != encoded || reference.ProcessID().String() != "process:parent" {
			t.Fatalf("reference %q = %+v, error=%v", encoded, reference, err)
		}
		data, err := json.Marshal(reference)
		if err != nil || string(data) != `"`+encoded+`"` {
			t.Fatalf("reference JSON = %s, error=%v", data, err)
		}
		var restored agent.ToolCallRef
		if decodeErr := json.Unmarshal(data, &restored); decodeErr != nil || restored != reference {
			t.Fatalf("restored reference=%v, error=%v", restored, decodeErr)
		}
		keyed := map[agent.ToolCallRef]string{reference: "result"}
		data, err = json.Marshal(keyed)
		if err != nil || string(data) != `{"`+encoded+`":"result"}` {
			t.Fatalf("reference map JSON = %s, error=%v", data, err)
		}
		var decoded map[agent.ToolCallRef]string
		if decodeErr := json.Unmarshal(data, &decoded); decodeErr != nil || decoded[reference] != "result" {
			t.Fatalf("restored reference map=%v, error=%v", decoded, decodeErr)
		}
	}
	var zero agent.ToolCallRef
	if zero.Valid() || zero.String() != "" {
		t.Fatal("zero reference claims an identity")
	}
	if _, err := json.Marshal(zero); !errors.Is(err, agent.ErrInvalidToolCallRef) {
		t.Fatalf("zero reference marshaled: %v", err)
	}
}

func TestToolCallRefRejectsNoncanonicalInput(t *testing.T) {
	valid, err := agent.ParseToolCallRef("process:parent/1/0")
	if err != nil {
		t.Fatal(err)
	}
	for _, encoded := range []string{
		"", "/1/0", "process:parent/0/0", "process:parent/1", "process:parent/1/0/extra",
		"process:parent/01/0", "process:parent/1/00", "process:parent/+1/0", "process:parent/1/-1",
		"process:parent/18446744073709551616/0", "process:parent/1/4294967296", " process:parent/1/0",
	} {
		if reference, err := agent.ParseToolCallRef(encoded); !errors.Is(err, agent.ErrInvalidToolCallRef) || reference.Valid() {
			t.Fatalf("accepted %q: %v, error=%v", encoded, reference, err)
		}
		unchanged := valid
		if err := unchanged.UnmarshalText([]byte(encoded)); !errors.Is(err, agent.ErrInvalidToolCallRef) || unchanged != valid {
			t.Fatalf("invalid input %q changed the destination: %v", encoded, err)
		}
	}
	var nilReference *agent.ToolCallRef
	if err := nilReference.UnmarshalText([]byte(valid.String())); !errors.Is(err, agent.ErrInvalidToolCallRef) {
		t.Fatalf("nil receiver = %v", err)
	}
}

func TestReferenceQueriesRejectAbsentAttribution(t *testing.T) {
	var invocation agent.ToolInvocation
	if reference, present := invocation.Reference(); present || reference.Valid() {
		t.Fatal("zero invocation invented a logical call")
	}
	var round agent.RoundResults
	if reference, present := round.Reference(0); present || reference.Valid() {
		t.Fatal("zero results invented a logical call")
	}
	var attribution agent.DelegateAttribution
	if reference, present := attribution.Reference(); present || reference.Valid() {
		t.Fatal("zero delegate invented a logical call")
	}
}
