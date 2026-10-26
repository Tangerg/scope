package a2a

import "go.opentelemetry.io/otel"

var a2aTracer = otel.Tracer("github.com/Tangerg/scope/a2a")

const attrAgentName = "gen_ai.agent.name"

const (
	attrTaskID    = "task.id"
	attrContextID = "context.id"
)
