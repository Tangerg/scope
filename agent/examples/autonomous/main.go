// Command autonomous demonstrates an Interaction in which the model chooses a
// Tool from environment feedback and decides when to stop.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/Tangerg/scope/agent/interaction"
	agentrun "github.com/Tangerg/scope/agent/run"
	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/core/tool"
)

const (
	calculatorToolRequestCall = 1
	calculatorFinalAnswerCall = 2
	additionToolName          = "add"
)

func main() {
	if err := run(context.Background(), os.Stdout); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, output io.Writer) (err error) {
	runtime, err := agentrun.New(ctx, agentrun.Config{Build: "example-autonomous", Store: agentrun.NewMemoryStore()})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, runtime.Close(context.WithoutCancel(ctx))) }()
	client, err := chatclient.New(&calculatorModel{}, chatclient.Config{})
	if err != nil {
		return err
	}
	addition, err := tool.NewFunc(tool.FuncConfig{Name: additionToolName, Description: "Add two numbers and return their sum."},
		func(_ context.Context, input additionInput) (string, error) {
			return fmt.Sprintf("%g", input.Left+input.Right), nil
		})
	if err != nil {
		return err
	}
	tools, err := interaction.NewToolSet(interaction.ToolSetConfig{
		Revision: "addition-v1", Tools: []tool.Tool{addition},
	})
	if err != nil {
		return err
	}
	program, err := interaction.Compile(runtime.Compiler(), interaction.Config{
		Name: "example.autonomous_calculator", Description: "Use available tools to complete the requested calculation.",
		Model: client, Tools: tools, MaxModelCalls: agentrun.NewQuota(calculatorFinalAnswerCall),
		Revision: "calculator-v1",
	})
	if err != nil {
		return err
	}

	decoded, err := runtime.Run(ctx, program, interaction.Input{Messages: []chat.Message{
		chat.NewUserMessage(chat.NewTextPart("What is 20 + 22?")),
	}})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(output, decoded.ModelResponse.Text())
	return err
}

type additionInput struct {
	Left  float64 `json:"left"`
	Right float64 `json:"right"`
}

type calculatorModel struct{ calls int }

func (c *calculatorModel) Call(_ context.Context, request *chat.Request) (*chat.Response, error) {
	c.calls++
	switch c.calls {
	case calculatorToolRequestCall:
		message := chat.NewAssistantMessage(chat.NewToolCallPart(chat.ToolCall{
			ID: "calculation_1", Name: additionToolName, Arguments: `{"left":20,"right":22}`,
		}))
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonToolCalls,
		}}, nil
	case calculatorFinalAnswerCall:
		last := request.Messages[len(request.Messages)-1]
		if last.Role != chat.RoleTool || len(last.Parts) != 1 || last.Parts[0].ToolResult == nil {
			return nil, errors.New("model did not receive the addition result")
		}
		result, ok := last.Parts[0].ToolResult.Output.Text()
		if !ok || result != "42" {
			return nil, errors.New("model did not receive the addition result")
		}
		message := chat.NewAssistantMessage(chat.NewTextPart("20 + 22 = 42"))
		return &chat.Response{Output: &chat.Output{
			Message: &message, FinishReason: chat.FinishReasonStop,
		}}, nil
	default:
		return nil, errors.New("interaction did not stop after receiving the Tool result")
	}
}
