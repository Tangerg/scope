package a2a

import (
	"context"
	"iter"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/samber/lo"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Agent is the text-only boundary served by [NewHTTPHandler].
type Agent interface {
	// Run handles one inbound A2A message, already flattened to text, and
	// yields the reply as a sequence of text chunks. A single-shot agent
	// yields once; a streaming agent yields deltas. A yielded error ends the
	// task as failed and stops iteration.
	Run(ctx context.Context, input string) iter.Seq2[string, error]
}

type executor struct {
	agent Agent
}

var _ a2asrv.AgentExecutor = (*executor)(nil)

// All deltas share one artifact ID so the SDK assembles a single result.
type textArtifact struct {
	id sdka2a.ArtifactID
}

func (t *textArtifact) append(info sdka2a.TaskInfoProvider, chunk string) *sdka2a.TaskArtifactUpdateEvent {
	part := sdka2a.NewTextPart(chunk)
	if t.id != "" {
		return sdka2a.NewArtifactUpdateEvent(info, t.id, part)
	}
	event := sdka2a.NewArtifactEvent(info, part)
	t.id = event.Artifact.ID
	return event
}

func newExecutor(agent Agent) (*executor, error) {
	if lo.IsNil(agent) {
		return nil, ErrNilAgent
	}
	return &executor{agent: agent}, nil
}

func (e *executor) Execute(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[sdka2a.Event, error] {
	return func(yield func(sdka2a.Event, error) bool) {
		projection := textProjection{}

		spanCtx, span := a2aTracer.Start(ctx, "a2a.agent.serve",
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String(attrTaskID, string(execCtx.TaskID)),
				attribute.String(attrContextID, execCtx.ContextID),
			),
		)
		defer span.End()

		input := ""
		if execCtx.Message != nil {
			input = projection.parts(execCtx.Message.Parts)
		}

		// A2A requires the task to exist before status or artifact events.
		if !yield(sdka2a.NewSubmittedTask(execCtx, execCtx.Message), nil) {
			return
		}
		if !yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateWorking, nil), nil) {
			return
		}

		fail := func(err error) {
			recordSpanError(span, err)
			message := sdka2a.NewMessage(sdka2a.MessageRoleAgent, sdka2a.NewTextPart(err.Error()))
			yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateFailed, message), nil)
		}
		sequence := e.agent.Run(spanCtx, input)
		if sequence == nil {
			fail(errNilAgentSequence)
			return
		}
		var artifact textArtifact
		for chunk, err := range sequence {
			if err != nil {
				fail(err)
				return
			}
			if chunk == "" {
				continue
			}
			if !yield(artifact.append(execCtx, chunk), nil) {
				return
			}
		}

		yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateCompleted, nil), nil)
	}
}

// The SDK cancels the in-flight Execute context itself; Cancel emits only the
// terminal task state so two independent cancellation paths cannot race over
// execution ownership.
func (e *executor) Cancel(ctx context.Context, execCtx *a2asrv.ExecutorContext) iter.Seq2[sdka2a.Event, error] {
	return func(yield func(sdka2a.Event, error) bool) {
		yield(sdka2a.NewStatusUpdateEvent(execCtx, sdka2a.TaskStateCanceled, nil), nil)
	}
}
