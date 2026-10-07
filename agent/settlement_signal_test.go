package agent

import (
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"
)

func TestSettlementSignalCarriesTheStatusThatOwnsSuccess(t *testing.T) {
	id := controlValue(ParseSignalID("signal:engine:settled"))
	failed := controlValue(NewSettlement(SettlementStatusFailed, []byte(`"refused"`)))
	signal := controlValue(NewSettlementSignal(id, failed))
	var decoded Signal
	if err := jsonv2.Unmarshal(controlValue(jsonv2.Marshal(signal)), &decoded); err != nil {
		t.Fatal(err)
	}
	settlement, err := ParseSettlement(decoded)
	if err != nil || !settlement.equal(failed) {
		t.Fatalf("settlement = %+v, %v; want %+v", settlement, err, failed)
	}
	if _, err := ParseSettlement(controlValue(NewSignal(id, WaitID{}, []byte(`"refused"`)))); !errors.Is(err, ErrInvalidSettlement) {
		t.Fatalf("Signal without a status parsed as a settlement: %v", err)
	}
	unknown := controlValue(NewSettlement(SettlementStatusUnknown, []byte(`{}`)))
	external := controlValue(ParseSignalID("signal:external"))
	for name, build := range map[string]func() (Signal, error){
		"unknown":  func() (Signal, error) { return NewSettlementSignal(id, unknown) },
		"external": func() (Signal, error) { return NewSettlementSignal(external, failed) },
	} {
		if _, err := build(); !errors.Is(err, ErrInvalidSignal) {
			t.Errorf("%s settlement delivery accepted: %v", name, err)
		}
	}
	for _, data := range []string{
		`{"id":"signal:external","status":"failed","payload":"refused"}`,
		`{"id":"signal:engine:settled","wait_id":"wait:x","status":"failed","payload":"refused"}`,
		`{"id":"signal:engine:settled","status":"unknown","payload":{}}`,
	} {
		var forged Signal
		if err := jsonv2.Unmarshal([]byte(data), &forged); !errors.Is(err, ErrInvalidSignal) {
			t.Errorf("%s decoded: %v", data, err)
		}
	}
}
