package interaction_test

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/agent"
	"github.com/Tangerg/scope/agent/agenttest"
	"github.com/Tangerg/scope/agent/strategy/interaction"
	"github.com/Tangerg/scope/core/chat"
)

type fixedResponseContext struct{ text string }

func (f fixedResponseContext) ReduceModelContext(context.Context, interaction.ModelInvocation, *chat.Request) ([]chat.Message, error) {
	return []chat.Message{chat.NewUserMessage(chat.NewTextPart(f.text))}, nil
}

func TestModelResponseLimitIncludesEncodingAndReplacementContext(t *testing.T) {
	for _, test := range []struct {
		name, text, replacement string
		stream                  bool
		beforeCall              bool
	}{
		{name: "aggregate escaped text", text: strings.Repeat("\x01", 100)},
		{name: "stream escaped text", text: strings.Repeat("\x01", 100), stream: true},
		{name: "replacement plus response", text: strings.Repeat("x", 200), replacement: strings.Repeat("y", 200)},
		{name: "replacement exceeds budget before call", text: "unused", replacement: strings.Repeat("\x01", 100), beforeCall: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			var calls atomic.Int32
			config := interaction.DispatcherConfig{MaxResponseBytes: 512, Model: chat.ModelFunc(func(context.Context, *chat.Request) (*chat.Response, error) {
				calls.Add(1)
				return textResponse(test.text), nil
			})}
			if test.stream {
				config.Model = nil
				config.Streamer = responseStream(streamTextChunk(test.text, chat.FinishReasonStop))
			}
			if test.replacement != "" {
				config.ModelContextReducer = fixedResponseContext{text: test.replacement}
			}
			deployment := configuredInteraction(t, interaction.DefinitionConfig{Name: "interaction.response_limit", Description: "Reject unusable model output.", MaxModelCalls: agent.NewQuota(1)}, config, interaction.ToolSetConfig{})
			events := &agenttest.ObservationRecorder{}
			engine, err := agent.NewEngine(agent.EngineConfig{EventListeners: []agent.EventListener{events}})
			if err != nil {
				t.Fatal(err)
			}
			defer engine.Close(context.WithoutCancel(ctx))
			process, err := engine.Start(ctx, deployment.Deployment, interactionInput(t, "bounded"))
			if err != nil {
				t.Fatal(err)
			}
			defer process.Kill(context.WithoutCancel(ctx), "test cleanup")
			event, err := events.AwaitEvent(ctx, func(event agent.Event) bool { _, ok := event.EffectFinished(); return ok })
			if err != nil {
				t.Fatal(err)
			}
			fact, _ := event.EffectFinished()
			if test.beforeCall {
				result, err := process.Await(ctx)
				if err != nil {
					t.Fatal(err)
				}
				assertInteractionHostFailure(t, result)
				if fact.SettlementStatus() != agent.SettlementStatusFailed || calls.Load() != 0 {
					t.Fatalf("status=%s calls=%d", fact.SettlementStatus(), calls.Load())
				}
			} else if fact.SettlementStatus() != agent.SettlementStatusUnknown {
				t.Fatalf("oversize output produced %s, want unknown", fact.SettlementStatus())
			}
		})
	}
}
