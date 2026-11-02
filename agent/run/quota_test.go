package run

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/jsonschema"
)

func ExampleQuota() {
	unlimited := Quota{}
	finite := NewQuota(3)
	zero := NewQuota(0)
	fmt.Println(unlimited.Allows(10001), finite.Allows(2, 1), finite.Allows(3, 1), zero.Allows(1))
	// Output: true true false false
}

func TestTreeQuotaMustAdmitRoot(t *testing.T) {
	if _, err := New(t.Context(), Config{Build: "quota-tests", Store: NewMemoryStore(), TreeLimits: TreeLimits{MaxTreeProcesses: NewQuota(0)}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("empty tree allowance accepted: %v", err)
	}
}

func TestMailboxCapacityFitsTransitionConsumption(t *testing.T) {
	if _, err := New(t.Context(), Config{Build: "quota-tests", Store: NewMemoryStore(), TreeLimits: TreeLimits{MaxPendingMessages: uint64(^uint32(0)) + 1}}); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("unrepresentable mailbox capacity accepted: %v", err)
	}
}

func TestQuotaPreservesUnlimitedFiniteAndZero(t *testing.T) {
	schema := controlValue(jsonschema.For[Quota]())
	for _, test := range []struct {
		name      string
		quota     Quota
		wire      string
		allowsOne bool
	}{
		{"unlimited", Quota{}, `{"maximum":null}`, true},
		{"zero", NewQuota(0), `{"maximum":0}`, false},
		{"finite", NewQuota(3), `{"maximum":3}`, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			encoded, err := jsonv2.Marshal(test.quota)
			if err != nil || string(encoded) != test.wire {
				t.Fatalf("encoding=%s error=%v", encoded, err)
			}
			if validateErr := schema.Validate(encoded); validateErr != nil {
				t.Fatalf("quota schema rejected its encoding: %v", validateErr)
			}
			var restored Quota
			if err := jsonv2.Unmarshal(encoded, &restored); err != nil || restored != test.quota {
				t.Fatalf("round trip=%+v error=%v", restored, err)
			}
			if restored.Allows(1) != test.allowsOne {
				t.Fatal("quota changed its bound")
			}
		})
	}
	for _, data := range []string{`0`, `null`, `{}`, `{"maximum":-1}`, `{"maximum":1,"extra":true}`, `{"maximum":18446744073709551616}`} {
		var quota Quota
		if err := jsonv2.Unmarshal([]byte(data), &quota); err == nil {
			t.Fatalf("invalid quota accepted: %s", data)
		}
	}
	if !NewQuota(^uint64(0)).Allows(^uint64(0)-1, 1) || NewQuota(^uint64(0)).Allows(^uint64(0), 1) {
		t.Fatal("finite quota arithmetic overflowed")
	}
	if !(Quota{}).Allows(^uint64(0), ^uint64(0)) {
		t.Fatal("unlimited quota became a machine-sized budget")
	}
}

func TestBudgetAllocationIsIndependentPerDimension(t *testing.T) {
	finite := Budget{Steps: NewQuota(10), Effects: NewQuota(10), Messages: NewQuota(10)}
	child := Budget{Steps: NewQuota(3), Effects: NewQuota(4), Messages: NewQuota(5)}
	for _, test := range []struct {
		name          string
		parent, child Budget
		debit         resourceAmounts
		allowed       bool
	}{
		{"finite finite", finite, child, resourceAmounts{Steps: 3, Effects: 4, Messages: 5}, true},
		{"unlimited finite", Budget{}, child, resourceAmounts{}, true},
		{"unlimited unlimited", Budget{}, Budget{}, resourceAmounts{}, true},
		{"finite unlimited", finite, Budget{}, resourceAmounts{}, false},
		{"mixed", Budget{Effects: NewQuota(10)}, Budget{Effects: NewQuota(4)}, resourceAmounts{Effects: 4}, true},
		{"mixed escalation", Budget{Effects: NewQuota(10)}, Budget{}, resourceAmounts{}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			debit, ok := test.parent.allocation(test.child)
			if ok != test.allowed || ok && debit != test.debit {
				t.Fatalf("debit=%+v allowed=%t", debit, ok)
			}
		})
	}
	if !finite.canAllocate(Usage{CommittedSteps: 6}, resourceAmounts{Steps: 1}, child) || finite.canAllocate(Usage{CommittedSteps: 7}, resourceAmounts{Steps: 1}, child) {
		t.Fatal("prepared parent work was not reserved at the exact boundary")
	}
}

func TestBudgetUsesOneStrictRepresentation(t *testing.T) {
	budget := Budget{Steps: NewQuota(5), Effects: NewQuota(7), Messages: NewQuota(9)}
	encoded := controlValue(jsonv2.Marshal(budget))
	var decoded Budget
	if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || decoded != budget {
		t.Fatalf("budget round trip=%+v, %v", decoded, err)
	}
	for _, field := range []string{"max_steps", "max_effects", "max_messages"} {
		var fields map[string]json.RawMessage
		if err := jsonv2.Unmarshal(encoded, &fields); err != nil {
			t.Fatal(err)
		}
		fields[field] = controlValue(jsonv2.Marshal(NewQuota(1)))
		if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(fields)), &decoded); err == nil {
			t.Fatalf("retired quota field %s accepted", field)
		}
	}
}
