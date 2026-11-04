package skills

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestReadOnlyToolsSettleSourceFailuresWithoutExposingDiagnostics(t *testing.T) {
	cause := errors.New("private source diagnostic")
	built, err := NewTools(failingSource{cause: cause}, Config{})
	if err != nil {
		t.Fatal(err)
	}
	for _, executable := range built {
		name := executable.Definition().Name
		t.Run(name, func(t *testing.T) {
			if executable.Definition().Effect != tool.EffectSafe {
				t.Errorf("effect=%q, want safe", executable.Definition().Effect)
			}
			arguments := `{}`
			switch name {
			case "load_skill":
				arguments = `{"name":"guide"}`
			case "read_skill_resource":
				arguments = `{"name":"guide","path":"ref.txt"}`
			}
			_, callErr := invokeTestTool(t.Context(), executable, arguments)
			failure, definite := tool.ClassifyFailure(callErr)
			if !definite {
				t.Fatalf("read-only failure remained unknown: %v", callErr)
			}
			text, _ := failure.Output().Text()
			if failure.Kind() != tool.FailureKindFailed || text != fmt.Sprintf("tool %q failed", name) || !errors.Is(failure.Cause(), cause) {
				t.Fatalf("kind=%s public=%q cause=%v", failure.Kind(), text, failure.Cause())
			}
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, canceledErr := invokeTestTool(ctx, executable, arguments)
			if _, definite := tool.ClassifyFailure(canceledErr); definite {
				t.Fatalf("canceled call became a definite failure: %v", canceledErr)
			}
		})
	}
}
