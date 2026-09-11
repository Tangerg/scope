package httpreq

import (
	"context"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

var _ toolcontract.Tool = (*Tool)(nil)

const toolName = "http_request"

// Tool adapts a configured [Client] to the tool contract. It takes a concrete
// client rather than an interface because the allowlist, method restrictions,
// and timeouts are the point of this tool; a substitutable transport would let
// a caller bypass them.
type Tool struct {
	client *Client
	inner  toolcontract.Tool
}

// NewTool rejects a nil client so a tool cannot be advertised to a model before
// the restrictions that make it safe exist.
func NewTool(client *Client) (*Tool, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	t := &Tool{client: client}
	inner, err := toolcontract.NewFunc[Request, *Response](
		toolcontract.FuncConfig{Name: toolName, Description: description},
		t.request,
	)
	if err != nil {
		return nil, fmt.Errorf("httpreq: build tool: %w", err)
	}
	t.inner = inner
	return t, nil
}

func (t *Tool) Definition() chat.ToolDefinition { return t.inner.Definition() }

// Unwrap exposes the typed input contract through tool decorators.
func (t *Tool) Unwrap() toolcontract.Tool { return t.inner }

const description = `Execute a single HTTP request and return the response.
- The "url" must be a fully-formed absolute http(s) URL.
- Method defaults to GET. Write methods (POST/PUT/PATCH/DELETE) only work when configured policy allows them.
- Configured policy restricts which hosts and methods are reachable. A policy rejection is final for that host and method; do not retry the same request.
- Response body is capped by the configured policy; when truncated, response.truncated == true.
- For body with JSON content, pass a JSON-encoded string as "body" and set Content-Type via "headers".
- Use this for arbitrary REST/JSON APIs. Prefer the dedicated web_search / web_fetch tools for general web pages.`

func (t *Tool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return t.inner.Call(ctx, invocation)
}

func (t *Tool) request(ctx context.Context, request Request) (*Response, error) {
	return t.client.Do(ctx, &request)
}
