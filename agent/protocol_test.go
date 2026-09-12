package agent

import (
	"encoding/json"
	"errors"
	"testing"
)

func TestSignalKeepsDeliveryAndWaitIdentitySeparate(t *testing.T) {
	signalID, err := ParseSignalID("signal:42")
	if err != nil {
		t.Fatal(err)
	}
	waitID, err := ParseWaitID("wait:7")
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(` { "answer": true } `)
	signal, err := newSignal(signalID, waitID, payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	if signal.ID() != signalID {
		t.Fatalf("Signal.ID() = %v, want %v", signal.ID(), signalID)
	}
	if got, ok := signal.WaitID(); !ok || got != waitID {
		t.Fatalf("Signal.WaitID() = %v, %t, want %v, true", got, ok, waitID)
	}
	if got := string(signal.Payload()); got != `{"answer":true}` {
		t.Fatalf("Signal.Payload() = %s", got)
	}
}

func TestSignalStrictJSONRoundTrip(t *testing.T) {
	signalID, _ := ParseSignalID("signal:1")
	signal, err := newSignal(signalID, WaitID{}, json.RawMessage(`{"kind":"steer"}`))
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(signal)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Signal
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if _, ok := decoded.WaitID(); ok {
		t.Fatal("decoded Signal unexpectedly has a WaitID")
	}
	if decoded.ID() != signal.ID() || string(decoded.Payload()) != string(signal.Payload()) {
		t.Fatalf("decoded Signal = %+v, want %+v", decoded, signal)
	}
	if err := json.Unmarshal([]byte(`{"id":"signal:1","payload":{},"unknown":true}`), &decoded); !errors.Is(err, ErrInvalidSignal) {
		t.Fatalf("unknown field error = %v, want ErrInvalidSignal", err)
	}
	if err := json.Unmarshal([]byte(`{"id":"signal:1","received_at":"2026-08-06T00:00:00Z","payload":{}}`), &decoded); !errors.Is(err, ErrInvalidSignal) {
		t.Fatalf("retired received_at error = %v, want ErrInvalidSignal", err)
	}
}

func TestSignalRequestProducesByteEquivalentStrategyInput(t *testing.T) {
	id, _ := ParseSignalID("signal:stable-redelivery")
	waitID, _ := ParseWaitID("wait:stable-redelivery")
	request, err := NewSignalRequest(id, waitID, json.RawMessage(` { "answer": true } `))
	if err != nil {
		t.Fatal(err)
	}
	first, err := request.signal()
	if err != nil {
		t.Fatal(err)
	}
	second, err := request.signal()
	if err != nil {
		t.Fatal(err)
	}
	firstJSON, err := json.Marshal(first)
	if err != nil {
		t.Fatal(err)
	}
	secondJSON, err := json.Marshal(second)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstJSON) != string(secondJSON) {
		t.Fatalf("repeated SignalRequest changed Strategy input: %s != %s", firstJSON, secondJSON)
	}
}

func TestDispatcherEffectIsOpaqueAndImmutable(t *testing.T) {
	payload := json.RawMessage(` { "operation": "model" } `)
	effect, err := NewDispatcherEffect(payload)
	if err != nil {
		t.Fatal(err)
	}
	payload[3] = 'x'
	copyOfPayload := effect.Payload()
	copyOfPayload[0] = '['
	if effect.Target() != EffectTargetDispatcher || string(effect.Payload()) != `{"operation":"model"}` {
		t.Fatalf("Effect = target %s payload %s", effect.Target(), effect.Payload())
	}

	data, err := json.Marshal(effect)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Effect
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Target() != EffectTargetDispatcher || string(decoded.Payload()) != string(effect.Payload()) {
		t.Fatalf("decoded Effect = %+v, want %+v", decoded, effect)
	}
}

func TestSettlementPreservesUnknownWithoutImplyingRetry(t *testing.T) {
	effectID, _ := ParseEffectID("process:1:step:2:effect:0")
	settlement, err := NewSettlement(effectID, SettlementStatusUnknown, json.RawMessage(`{"reason":"connection_lost"}`))
	if err != nil {
		t.Fatal(err)
	}
	if settlement.Status() != SettlementStatusUnknown || settlement.EffectID() != effectID {
		t.Fatalf("Settlement = status %s effect %v", settlement.Status(), settlement.EffectID())
	}
	data, err := json.Marshal(settlement)
	if err != nil {
		t.Fatal(err)
	}
	var decoded Settlement
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Status() != SettlementStatusUnknown || string(decoded.Payload()) != `{"reason":"connection_lost"}` {
		t.Fatalf("decoded Settlement = %+v", decoded)
	}
}

func TestWaitRequestKeepsEngineKeySeparateFromStrategySignalPayload(t *testing.T) {
	key, _ := ParseWaitKey("approval:tool:1")
	effect, err := RequestWait(key, json.RawMessage(`{"kind":"approval_opened","tool":"shell"}`))
	if err != nil {
		t.Fatal(err)
	}
	if effect.Target() != EffectTargetFramework {
		t.Fatalf("RequestWait target = %s, want framework", effect.Target())
	}
	decodedKey, signalPayload, err := effect.waitRequest()
	if err != nil {
		t.Fatal(err)
	}
	if decodedKey != key || string(signalPayload) != `{"kind":"approval_opened","tool":"shell"}` {
		t.Fatalf("decoded wait request = key %v payload %s", decodedKey, signalPayload)
	}

	data, err := json.Marshal(effect)
	if err != nil {
		t.Fatal(err)
	}
	var restored Effect
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	if _, _, err := restored.waitRequest(); err != nil {
		t.Fatal(err)
	}
}

func TestFrameworkEffectRejectsUnknownOperations(t *testing.T) {
	data := []byte(`{"target":"framework","payload":{"operation":"tool","key":"approval","signal_payload":{}}}`)
	var effect Effect
	if err := json.Unmarshal(data, &effect); !errors.Is(err, ErrInvalidEffect) {
		t.Fatalf("unknown Framework Effect error = %v, want ErrInvalidEffect", err)
	}
}

func TestEffectEqualityUsesTheCompleteCanonicalRequest(t *testing.T) {
	decode := func(raw string) Effect {
		t.Helper()
		var value Effect
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	left := decode(`{"target":"dispatcher","payload":{"b":2,"a":1},"required_capabilities":["tool.read","tool.write"]}`)
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"canonical content", `{"target":"dispatcher","payload":{ "a":1,"b":2 },"required_capabilities":["tool.write","tool.read"]}`, true},
		{"different payload", `{"target":"dispatcher","payload":{"a":2,"b":2},"required_capabilities":["tool.read","tool.write"]}`, false},
		{"different authority", `{"target":"dispatcher","payload":{"a":1,"b":2},"required_capabilities":["tool.read"]}`, false},
		{"invalid request", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			right := decode(test.raw)
			if left.equal(right) != test.want || right.equal(left) != test.want {
				t.Fatalf("request equality must be %t in both directions", test.want)
			}
		})
	}
	framework := decode(`{"target":"framework","payload":{"operation":"wait","key":"approval","signal_payload":{}}}`)
	dispatcher, err := NewDispatcherEffect(framework.Payload())
	if err != nil {
		t.Fatal(err)
	}
	if framework.equal(dispatcher) || (Effect{}).equal(Effect{}) {
		t.Fatal("different targets or invalid requests must not match")
	}
}

func TestSettlementEqualityKeepsIdentityStatusAndPayload(t *testing.T) {
	decode := func(raw string) Settlement {
		t.Helper()
		var value Settlement
		if raw != "" {
			if err := json.Unmarshal([]byte(raw), &value); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	left := decode(`{"effect_id":"effect:one","status":"unknown","payload":{"b":2,"a":1}}`)
	for _, test := range []struct {
		name string
		raw  string
		want bool
	}{
		{"canonical content", `{"effect_id":"effect:one","status":"unknown","payload":{ "a":1,"b":2 }}`, true},
		{"different identity", `{"effect_id":"effect:two","status":"unknown","payload":{"a":1,"b":2}}`, false},
		{"definite success", `{"effect_id":"effect:one","status":"succeeded","payload":{"a":1,"b":2}}`, false},
		{"definite failure", `{"effect_id":"effect:one","status":"failed","payload":{"a":1,"b":2}}`, false},
		{"different payload", `{"effect_id":"effect:one","status":"unknown","payload":{"a":2,"b":2}}`, false},
		{"invalid settlement", "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			right := decode(test.raw)
			if left.equal(right) != test.want || right.equal(left) != test.want {
				t.Fatalf("settlement equality must be %t in both directions", test.want)
			}
		})
	}
	if (Settlement{}).equal(Settlement{}) {
		t.Fatal("invalid settlements must not match")
	}
}
