package workflow

import "github.com/Tangerg/scope/agent/run"

const childKeyNamespace = "workflow.child"

func workflowChildKey(parts ...string) (run.ChildKey, error) {
	return run.DeriveChildKey(childKeyNamespace, parts...)
}
