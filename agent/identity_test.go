package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestIdentityTypesRemainDistinctAndRoundTrip(t *testing.T) {
	processID, err := ParseProcessID("process:01J1-test")
	if err != nil {
		t.Fatal(err)
	}
	data, err := jsonv2.Marshal(processID)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(data); got != `"process:01J1-test"` {
		t.Fatalf("jsonv2.Marshal(ProcessID) = %s", got)
	}
	var decoded ProcessID
	if err := jsonv2.Unmarshal(data, &decoded); err != nil {
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
	if _, err := jsonv2.Marshal(ProcessID{}); !errors.Is(err, ErrInvalidIdentity) {
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
		{"effect", effect.String(), "effect:d8cddbe8d7ad503ff7572edfba782c45be4db4202e268fc7a0f2ca811928dbea"},
		{"wait", wait.String(), "wait:5d85909a182fa8637668521fe62d90a8d52f6a39ba536189fc7220be83e5dc05"},
		{"settlement", effect.settlementSignalID().String(), "signal:engine:7f1581661209cd8d06489b89141ab8c4ee899201e76425274bbcb6916a1704d3"},
		{"child", effect.childProcessID().String(), "process:03ff1c6bbd2c7dc9921bb773edb3d43f0ded05ecdfadadf71eebeb20a92d449c"},
		{"child wait", wait.childWaitSignalID().String(), "signal:engine:8efa9947851ff38baf5240e39c88b2cfc9437964330b76557fe8a0ac9845cff0"},
	} {
		t.Run(test.name, func(t *testing.T) {
			if test.got != test.want {
				t.Fatalf("identity = %q, want %q", test.got, test.want)
			}
		})
	}
}
