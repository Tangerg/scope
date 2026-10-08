package httpreq

import (
	"context"
	"errors"
	"fmt"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

var _ toolcontract.Tool = (*Tool)(nil)

const toolName = "http_request"

// Tool requires a Client so every invocation enforces its frozen network policy.
type Tool struct {
	client *Client
	inner  toolcontract.Tool
}

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

func (t *Tool) Unwrap() toolcontract.Tool { return t.inner }

const description = `Execute a single HTTP request and return the response.
- The "url" must be a fully-formed absolute http(s) URL.
- Method defaults to GET. Write methods (POST/PUT/PATCH/DELETE) only work when configured policy allows them.
- Configured policy restricts which hosts and methods are reachable. A policy rejection is final for that host and method; do not retry the same request.
- Response body is capped by the configured policy; when truncated, response.truncated == true.
- Body and header values contain encoding and data: utf8 is readable text; base64 preserves non-UTF8 bytes.
- For body with JSON content, pass a JSON-encoded string as "body" and set Content-Type via "headers".
- Use this for arbitrary REST/JSON APIs. Prefer the dedicated web_search / web_fetch tools for general web pages.`

func (t *Tool) Call(ctx context.Context, invocation toolcontract.Invocation) (chat.ToolOutput, error) {
	return t.inner.Call(ctx, invocation)
}

func (t *Tool) request(ctx context.Context, request Request) (*Response, error) {
	response, err := t.client.Do(ctx, &request)
	if admission, ok := errors.AsType[*admissionError](err); ok {
		return nil, admission.failure()
	}
	if err != nil && response != nil {
		return nil, toolresult.WithEvidence("httpreq: request", response, err)
	}
	return response, err
}
