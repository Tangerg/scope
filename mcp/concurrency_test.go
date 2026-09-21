package mcp_test

import (
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	toolcontract "github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestAnnotatedReadOnlyConcurrencyPolicy(t *testing.T) {
	tests := []struct {
		name        string
		annotations sdkmcp.ToolAnnotations
		concurrent  bool
	}{
		{name: "missing annotations"},
		{
			name: "idempotent mutation is still exclusive",
			annotations: sdkmcp.ToolAnnotations{
				IdempotentHint: true,
			},
		},
		{
			name: "explicit read only",
			annotations: sdkmcp.ToolAnnotations{
				ReadOnlyHint: true,
			},
			concurrent: true,
		},
		{
			name: "contradictory destructive hint fails closed",
			annotations: sdkmcp.ToolAnnotations{
				ReadOnlyHint:    true,
				DestructiveHint: new(true),
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			key, concurrent := scopemcp.AnnotatedReadOnlyConcurrencyPolicy(
				"source", "tool", test.annotations, toolcontract.Invocation{},
			)
			if key != "" || concurrent != test.concurrent {
				t.Fatalf("AnnotatedReadOnlyConcurrencyPolicy() = %q, %t, want empty key, %t", key, concurrent, test.concurrent)
			}
		})
	}
}
