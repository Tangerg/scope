package coordination

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/jsonschema"
)

func TestInputGateValidatesAnswerLimitAtCompilation(t *testing.T) {
	compiler := conformancetest.Compiler(t)
	schema := require(jsonschema.For[string]())
	for _, maximum := range []int{-1, 0, 1, run.MaxPayloadBytes, run.MaxPayloadBytes + 1} {
		program, err := Compile[string, string](compiler, Config{
			Name: "test.input_limit", Description: "Admit only deliverable answers.",
			Policy: InputGate{RequestSchema: schema, AnswerSchema: schema, MaxAnswerBytes: maximum},
		})
		valid := maximum >= 0 && maximum <= run.MaxPayloadBytes
		if valid && (err != nil || !program.Descriptor().OutputSchema().Equal(schema)) {
			t.Fatalf("valid answer limit %d: %v", maximum, err)
		}
		if !valid && !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("invalid answer limit %d passed compilation: %v", maximum, err)
		}
	}
}

func TestInputGateAdmittedMaximumAnswerCompletes(t *testing.T) {
	runtime := conformancetest.Runtime(t, run.Config{})
	schema := require(jsonschema.For[string]())
	program := require(Compile[string, string](runtime.Compiler(), Config{
		Name: "test.input_maximum", Description: "Return the admitted answer without another envelope.",
		Policy: InputGate{RequestSchema: schema, AnswerSchema: schema, MaxAnswerBytes: inputGateTestAnswerBytes},
	}))
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	handle := require(runtime.Start(ctx, program, run.StartRequest[string]{ID: run.StartID{Scope: "test", Key: t.Name()}, Input: "question"}))
	wait := waitFor(t, handle)
	answer := strings.Repeat("a", inputGateTestAnswerBytes-2)
	payload := require(run.EncodePayload(answer))
	signal := run.Signal{ID: require(run.ParseSignalID("delivery-specific-identity")), Wait: wait.Key, Payload: payload}
	if accepted, err := handle.DeliverSignals(ctx, signal); !accepted || err != nil {
		t.Fatalf("maximum answer admission: accepted=%v error=%v", accepted, err)
	}
	result := require(handle.Await(ctx))
	output, err := result.Output()
	if err != nil || output != answer {
		t.Fatalf("admitted maximum answer was not delivered: status=%s error=%v", result.Termination().Status(), err)
	}
	if confirmed, err := runtime.Answered(ctx, handle.ID(), wait.Key, payload); !confirmed || err != nil {
		t.Fatalf("completion lost its admitted identity: confirmed=%v error=%v", confirmed, err)
	}
}
