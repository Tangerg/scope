package tool_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

type capabilityMarker interface {
	Marker() string
}

type markedTool struct{}

func (markedTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{Name: "marked", InputSchema: json.RawMessage(`{"type":"object"}`)}
}

func (markedTool) Call(context.Context, tool.Invocation) (tool.Result, error) {
	return tool.Result{}, nil
}
func (markedTool) Marker() string { return "inner" }

type wrappingTool struct{ inner tool.Tool }

func (w *wrappingTool) Definition() chat.ToolDefinition { return w.inner.Definition() }
func (w *wrappingTool) Call(ctx context.Context, input tool.Invocation) (tool.Result, error) {
	return w.inner.Call(ctx, input)
}
func (w *wrappingTool) Unwrap() tool.Tool { return w.inner }

func TestCapabilityTraversesWrappingTools(t *testing.T) {
	value := &wrappingTool{inner: &wrappingTool{inner: markedTool{}}}
	marker, ok, err := tool.Capability[capabilityMarker](value)
	if err != nil || !ok || marker.Marker() != "inner" {
		t.Fatalf("Capability() = %v, %v, %v; want inner, true, nil", marker, ok, err)
	}
}

func TestCapabilityRejectsWrappingCycles(t *testing.T) {
	for name, chainOf := range map[string]func() tool.Tool{
		"two wrappers naming each other": func() tool.Tool {
			first := &wrappingTool{}
			second := &wrappingTool{inner: first}
			first.inner = second
			return first
		},
		"a wrapper naming itself": func() tool.Tool {
			self := &wrappingTool{}
			self.inner = self
			return self
		},
	} {
		t.Run(name, func(t *testing.T) {
			type outcome struct {
				found bool
				err   error
			}
			done := make(chan outcome, 1)
			go func() {
				_, found, err := tool.Capability[capabilityMarker](chainOf())
				done <- outcome{found: found, err: err}
			}()

			select {
			case got := <-done:
				if got.found || !errors.Is(got.err, tool.ErrInvalidWrappingChain) {
					t.Fatalf("Capability() = _, %v, %v; want false, ErrInvalidWrappingChain", got.found, got.err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("Capability did not return on a wrapping chain that never ends")
			}
		})
	}
}
