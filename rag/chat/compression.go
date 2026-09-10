package chat

import (
	"context"
	"fmt"
	"strings"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/chatclient"
	"github.com/Tangerg/scope/rag"
)

// compressionDefaultTemplate asks the LLM to fold a chat history plus a
// follow-up question into one self-contained query. {{.History}} and
// {{.Query}} are filled at transform time.
const compressionDefaultTemplate = `Given the following conversation history and a follow-up query, your task is to synthesize
a concise, standalone query that incorporates the context from the history.
Ensure the standalone query is clear, specific, and maintains the user's intent.

Conversation history:
{{.History}}

Follow-up query:
{{.Query}}

Standalone query:`

// CompressionTransformerConfig binds a chat model and token budget to one
// history-aware query compression policy.
type CompressionTransformerConfig struct {
	// Model performs the compression. Required.
	Model corechat.Model

	// PromptTemplate is the LLM prompt. Defaults to
	// [compressionDefaultTemplate]. Custom templates must declare
	// {{.History}} and {{.Query}}.
	PromptTemplate *chatclient.Template
}

var _ rag.Transformer = (*CompressionTransformer)(nil)

// CompressionTransformer turns conversation history and a follow-up into one
// self-contained query.
type CompressionTransformer struct {
	prompt textModelPrompt
}

type compressionPromptVariables struct {
	History string
	Query   string
}

// NewCompressionTransformer validates and freezes the model-backed compression
// boundary.
func NewCompressionTransformer(config CompressionTransformerConfig) (*CompressionTransformer, error) {
	prompt, err := newTextModelPrompt(
		config.Model,
		config.PromptTemplate,
		compressionDefaultTemplate,
		promptVariableHistory,
		promptVariableQuery,
	)
	if err != nil {
		return nil, err
	}

	return &CompressionTransformer{prompt: prompt}, nil
}

// Transform asks the LLM for a self-contained version of the query.
// Returns a clone of the input with Text replaced by the LLM output.
func (c *CompressionTransformer) Transform(ctx context.Context, query rag.Query) (rag.Query, error) {
	if err := query.Validate(); err != nil {
		return rag.Query{}, err
	}

	history, err := c.extractHistory(ctx, query)
	if err != nil {
		return rag.Query{}, err
	}

	compressed, err := c.prompt.call(ctx, compressionPromptVariables{
		History: history,
		Query:   query.Text(),
	})
	if err != nil {
		return rag.Query{}, err
	}

	return query.WithText(compressed)
}

// extractHistory pulls the conversation messages out of the query value under
// [HistoryValueKey] and renders them as one string.
// Returns "" when the slot is missing.
func (c *CompressionTransformer) extractHistory(ctx context.Context, query rag.Query) (string, error) {
	messages, exists, err := query.Value(historyValueKey)
	if err != nil {
		return "", fmt.Errorf("rag: read chat history: %w", err)
	}
	if !exists {
		return "", nil
	}
	return c.formatHistory(ctx, messages)
}

func (c *CompressionTransformer) formatHistory(ctx context.Context, messages []corechat.Message) (string, error) {
	var output strings.Builder
	for messageIndex := range messages {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if err := messages[messageIndex].Validate(); err != nil {
			return "", fmt.Errorf("rag: format chat history message %d: %w", messageIndex, err)
		}
		if output.Len() != 0 {
			output.WriteString("\n\n")
		}
		fmt.Fprintf(&output, "%s: ", messages[messageIndex].Role)
		for partIndex := range messages[messageIndex].Parts {
			if partIndex != 0 {
				output.WriteString(" ")
			}
			part := messages[messageIndex].Parts[partIndex]
			switch part.Kind {
			case corechat.PartText:
				output.WriteString(part.Text)
			case corechat.PartMedia:
				fmt.Fprintf(&output, "[media %s]", part.Media.MIME)
			case corechat.PartReasoning:
				output.WriteString("[reasoning omitted]")
			case corechat.PartToolCall:
				fmt.Fprintf(&output, "[tool call %s %s]", part.ToolCall.Name, part.ToolCall.Arguments)
			case corechat.PartToolResult:
				if text, ok := part.ToolResult.Output.Text(); ok {
					fmt.Fprintf(&output, "[tool result %s %s]", part.ToolResult.Name, text)
				} else {
					fmt.Fprintf(&output, "[tool result %s contains media]", part.ToolResult.Name)
				}
			case corechat.PartRefusal:
				output.WriteString(part.Text)
			}
		}
	}
	return output.String(), nil
}
