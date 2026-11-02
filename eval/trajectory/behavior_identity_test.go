package trajectory

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

func TestBehaviorProjectionPreservesContinuationBindingWithoutRootIdentity(t *testing.T) {
	root := run.StartID{Scope: "test", Key: "first"}.Root()
	call := ToolCall{EffectID: run.EffectID{Process: root, Step: 5}, Call: chat.ToolCall{ID: "provider-call", Name: "weather", Arguments: `{}`}}
	independent, err := behaviorToolOf(call, rootProcessPath)
	if err != nil {
		t.Fatal(err)
	}
	call.ContinuationOf = run.EffectID{Process: root, Step: 2, Index: 1}
	continued, err := behaviorToolOf(call, rootProcessPath)
	if err != nil {
		t.Fatal(err)
	}
	first, err := jsonv2.Marshal(independent)
	if err != nil {
		t.Fatal(err)
	}
	second, err := jsonv2.Marshal(continued)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(first, second) {
		t.Fatal("an independent call and continuation acquired the same semantic behavior")
	}
	replacement := run.StartID{Scope: "test", Key: "second"}.Root()
	call.EffectID.Process, call.ContinuationOf.Process = replacement, replacement
	projected, err := behaviorToolOf(call, rootProcessPath)
	if err != nil {
		t.Fatal(err)
	}
	third, err := jsonv2.Marshal(projected)
	if err != nil || !bytes.Equal(second, third) {
		t.Fatal("an independent runtime root changed the same continuation behavior", err)
	}
	call.ContinuationOf.Process = root
	if _, projectionErr := behaviorToolOf(call, rootProcessPath); !errors.Is(projectionErr, ErrInvalidTrajectory) {
		t.Fatal("foreign continuation acquired a local semantic identity", projectionErr)
	}
}
