package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestIdentityTypesRemainDistinctAndRoundTrip(t *testing.T) {
	processID, err := ParseProcessID("process:01J1-test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(processID)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `"process:01J1-test"` {
		t.Fatalf("json.Marshal(ProcessID) = %s", got)
	}
	var decoded ProcessID
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded != processID {
		t.Fatalf("decoded ProcessID = %q, want %q", decoded, processID)
	}
}

func TestIdentityRejectsEmptyUnsafeAndOversizedValues(t *testing.T) {
	values := []string{"", "contains space", "contains/slash", string(make([]byte, maxIdentityBytes+1))}
	for _, value := range values {
		if _, err := ParseSignalID(value); !errors.Is(err, ErrInvalidIdentity) {
			t.Fatalf("ParseSignalID(%q) error = %v, want ErrInvalidIdentity", value, err)
		}
	}
	if _, err := json.Marshal(ProcessID{}); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("marshal zero ProcessID error = %v, want ErrInvalidIdentity", err)
	}
}

func TestDerivedIdentitiesKeepTheirRecoveryKeys(t *testing.T) {
	process, err := ParseProcessID("process:reference")
	if err != nil {
		t.Fatal(err)
	}
	effect := process.effectID(7, 2)
	wait := effect.waitID()
	for _, test := range []struct {
		name string
		got  string
		want string
	}{
		{"effect", effect.String(), "effect:bfa789c0c8ff3c09cf8407b0cea89dbb1ff2d24c68918bf612e527543ab73c25"},
		{"wait", wait.String(), "wait:497ab3c74e18a145928f2dff14e543041549fe0b60e6a41abc054ed1ffb812ad"},
		{"settlement", effect.settlementSignalID().String(), "signal:ef156fb8bfbeca9caf77c35e9736b7bccccb0852ecb2a67c0e0d36eb5158493a"},
		{"child", effect.childProcessID().String(), "process:bbab1849326e74684e98f5f39e26f4db6b4003e60f8c9e4d00d5c2d61437ae1f"},
		{"child wait", wait.childWaitSignalID().String(), "signal:0e9c6cb8b0a508f93227f10a7dd835ed0887cd0431afd21ff9c8f59b1b25da19"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("identity = %q, want %q", test.got, test.want)
			}
		})
	}
}
