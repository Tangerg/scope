package agent

import (
	"bytes"
	"errors"
	"testing"

	jsonv2 "encoding/json/v2"
)

func TestPreparedEffectRejectsInvalidLifecycleCommandsWithoutChangingEvidence(t *testing.T) {
	processID := controlValue(ParseProcessID("process:effect-command"))
	effect := controlValue(NewDispatcherEffect([]byte(`{"operation":"write"}`)))
	succeeded := controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`"written"`)))
	unknown := controlValue(NewSettlement(SettlementStatusUnknown, []byte(`null`)))
	for _, test := range []struct {
		name    string
		prepare func(*preparedEffect)
		command func(*preparedEffect) error
	}{
		{"settle without permission", nil, func(p *preparedEffect) error { return p.settle(succeeded, nil) }},
		{"resolve without outcome", nil, func(p *preparedEffect) error { return p.resolveUnknown(succeeded) }},
		{"revoke absent permission", nil, (*preparedEffect).revokeDispatch},
		{"begin twice", func(p *preparedEffect) { _ = p.begin() }, (*preparedEffect).begin},
		{"invalid settlement", func(p *preparedEffect) { _ = p.begin() }, func(p *preparedEffect) error { return p.settle(Settlement{}, nil) }},
		{"resolve definite outcome", func(p *preparedEffect) { _ = p.begin(); _ = p.settle(succeeded, nil) }, func(p *preparedEffect) error { return p.resolveUnknown(succeeded) }},
		{"resolve unknown with unknown", func(p *preparedEffect) { _ = p.begin(); _ = p.settle(unknown, nil) }, func(p *preparedEffect) error { return p.resolveUnknown(unknown) }},
		{"resolve unknown with invalid outcome", func(p *preparedEffect) { _ = p.begin(); _ = p.settle(unknown, nil) }, func(p *preparedEffect) error { return p.resolveUnknown(Settlement{}) }},
		{"revoke completed permission", func(p *preparedEffect) { _ = p.begin(); _ = p.settle(succeeded, nil) }, (*preparedEffect).revokeDispatch},
	} {
		t.Run(test.name, func(t *testing.T) {
			record := preparedEffect{ID: processID.effectID(1, 0), Effect: effect}
			if test.prepare != nil {
				test.prepare(&record)
			}
			before := controlValue(jsonv2.Marshal(controlValue(record.wire())))
			if err := test.command(&record); err == nil {
				t.Fatal("invalid command accepted")
			}
			after := controlValue(jsonv2.Marshal(controlValue(record.wire())))
			if !bytes.Equal(before, after) {
				t.Fatalf("rejected command changed evidence: %s -> %s", before, after)
			}
		})
	}
}

func TestPreparedEffectResolutionPreservesTheOriginalUncertaintyDiagnostic(t *testing.T) {
	processID := controlValue(ParseProcessID("process:resolved-diagnostic"))
	record := preparedEffect{ID: processID.effectID(1, 0), Effect: controlValue(NewDispatcherEffect([]byte(`null`)))}
	if err := record.begin(); err != nil {
		t.Fatal(err)
	}
	unknown := controlValue(NewSettlement(SettlementStatusUnknown, []byte(`null`)))
	if err := record.settle(unknown, errors.New("remote acknowledgment lost")); err != nil {
		t.Fatal(err)
	}
	diagnostic := *record.diagnostic()
	resolved := controlValue(NewSettlement(SettlementStatusSucceeded, []byte(`"confirmed"`)))
	if err := record.resolveUnknown(resolved); err != nil {
		t.Fatal(err)
	}
	if !record.definitelySettled() || record.unknown() || record.diagnostic() == nil || *record.diagnostic() != diagnostic {
		t.Fatal("adjudication erased the attempt diagnostic or retained uncertainty")
	}
	projection, growth, err := record.snapshotReservation()
	if err != nil || growth != 0 || !projection.definitelySettled() {
		t.Fatalf("definite outcome still reserves unknown growth: %d, %v", growth, err)
	}
}
