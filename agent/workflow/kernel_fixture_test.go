package workflow

import "github.com/Tangerg/scope/agent/run"

func testTurn(step uint64) run.Turn {
	process, _ := run.ParseProcessID("workflow-test")
	return run.Turn{Process: process, Step: step}
}
