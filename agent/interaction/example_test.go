package interaction_test

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/agent/interaction"
	"github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
)

// summarizer compacts the retained context through a model once it holds more
// than limit messages, and keeps each summary in a host-owned memory.
type summarizer struct {
	limit  int
	model  chat.Model
	memory map[run.EffectID]string
}

func (s summarizer) PreparationTrigger() func(uint64, *chat.Request) bool {
	limit := s.limit // capture the value, never the preparer or its model
	return func(_ uint64, request *chat.Request) bool { return len(request.Messages) > limit }
}

func (s summarizer) PrepareContext(ctx context.Context, invocation interaction.ContextInvocation, request *chat.Request) ([]chat.Message, error) {
	response, err := s.model.Call(ctx, &chat.Request{Messages: request.Messages})
	if err != nil {
		return nil, err
	}
	// Key host writes by the Effect identity, which is stable across recovery.
	s.memory[invocation.Effect.ID()] = response.Text()
	latest := request.Messages[len(request.Messages)-1]
	return []chat.Message{chat.NewUserMessage(chat.NewTextPart(response.Text())), latest}, nil
}

func ExampleContextPreparer() {
	ctx := context.Background()
	runtime := exampleValue(run.New(ctx, run.Config{Build: "example", Store: run.NewMemoryStore()}))
	defer func() {
		if err := runtime.Close(ctx); err != nil {
			panic(err)
		}
	}()
	summaries := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		return reply(fmt.Sprintf("summary of %d messages", len(request.Messages))), nil
	})
	assistant := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		return reply(fmt.Sprintf("answered from %d messages, the first %q", len(request.Messages), request.Messages[0].Text())), nil
	})
	memory := make(map[run.EffectID]string)
	program := exampleValue(interaction.Compile(runtime.Compiler(), interaction.Config{
		Name: "example.compacting", Description: "Answer from a compacted history.",
		// Revision identifies the trigger's limit, a callback the Step replays.
		Revision:        "compacting/limit-2",
		Model:           interaction.CallModel(assistant),
		ContextPreparer: summarizer{limit: 2, model: summaries, memory: memory},
	}))
	input := interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("one")),
		chat.NewUserMessage(chat.NewTextPart("two")),
		chat.NewUserMessage(chat.NewTextPart("three")),
	}}
	output := exampleValue(runtime.Run(ctx, program, input))
	fmt.Println(output.ModelResponse.Text())
	fmt.Println(len(memory), "summary kept")
	// Output:
	// answered from 2 messages, the first "summary of 3 messages"
	// 1 summary kept
}

func reply(text string) *chat.Response {
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	return &chat.Response{Output: &chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}}
}

func exampleValue[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func ExampleContextPolicy() {
	ctx := context.Background()
	runtime := exampleValue(run.New(ctx, run.Config{Build: "example", Store: run.NewMemoryStore()}))
	defer func() {
		if err := runtime.Close(ctx); err != nil {
			panic(err)
		}
	}()
	// keepRecent retains the newest two messages. It is deterministic and
	// bounded, so the Step replays it on recovery without repeating any work.
	keepRecent := func(request *chat.Request) ([]chat.Message, error) {
		return request.Messages[max(0, len(request.Messages)-2):], nil
	}
	assistant := chat.ModelFunc(func(_ context.Context, request *chat.Request) (*chat.Response, error) {
		return reply(fmt.Sprintf("answered from %d messages, the first %q", len(request.Messages), request.Messages[0].Text())), nil
	})
	program := exampleValue(interaction.Compile(runtime.Compiler(), interaction.Config{
		Name: "example.recent", Description: "Answer from the most recent messages.",
		// Revision identifies keepRecent, a callback the Step replays.
		Revision:      "recent/2",
		Model:         interaction.CallModel(assistant),
		ContextPolicy: keepRecent,
	}))
	input := interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("one")),
		chat.NewUserMessage(chat.NewTextPart("two")),
		chat.NewUserMessage(chat.NewTextPart("three")),
	}}
	output := exampleValue(runtime.Run(ctx, program, input))
	fmt.Println(output.ModelResponse.Text())
	// Output: answered from 2 messages, the first "two"
}
