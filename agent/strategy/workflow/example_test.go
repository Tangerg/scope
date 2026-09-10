package workflow_test

import (
	"context"
	"fmt"
	"strings"

	"github.com/Tangerg/scope/agent/strategy/workflow"
)

func ExampleTransform() {
	stage, err := workflow.Transform("normalize", func(_ context.Context, input string) (string, error) {
		return strings.ToUpper(input), nil
	})
	if err != nil {
		panic(err)
	}

	fmt.Println(stage.Valid())
	// Output:
	// true
}
