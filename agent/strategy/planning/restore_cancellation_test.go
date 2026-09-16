package planning

import (
	"context"
	"errors"
	"testing"

	"github.com/Tangerg/scope/agent/internal/conformancetest"
)

func TestRestoreStopsBetweenActionAttempts(t *testing.T) {
	definition := &Definition{bindingsByName: map[string]int{"action": 0}, bindings: []ActionBinding{{}}}
	ctx, cancel := conformancetest.CancelAfterCheck(t.Context(), 2)
	defer cancel()
	attempts := []Attempt{{ActionName: "action", Status: AttemptSucceeded}, {}}
	if err := definition.validateActionHistory(ctx, attempts); !errors.Is(err, context.Canceled) {
		t.Fatalf("history validation = %v, want cancellation before malformed second attempt", err)
	}
}
