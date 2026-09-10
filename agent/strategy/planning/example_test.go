package planning_test

import (
	"fmt"

	"github.com/Tangerg/scope/agent/strategy/planning"
)

func ExampleNewWorldState() {
	ready, err := planning.NewCondition("service.ready", planning.True)
	if err != nil {
		panic(err)
	}
	state, err := planning.NewWorldState(ready)
	if err != nil {
		panic(err)
	}

	fmt.Println(state.Truth("service.ready"), state.Truth("service.cached"), state.Satisfies(ready))
	// Output:
	// true unknown true
}
