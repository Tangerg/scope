package httpreq

import (
	"context"
	"errors"
	"fmt"

	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/internal/toolresult"
)

var _ toolcontract.Tool = (*Tool)(nil)

const toolName = "http_request"

// requestFunc is embedded under an unexported name so the tool keeps its
// Func private while promoting the Tool methods.
type requestFunc = toolcontract.Func[Request, *Response]

// Tool requires a Client so every invocation enforces its frozen network policy.
type Tool struct {
	requestFunc
	client *Client
}

func NewTool(client *Client) (*Tool, error) {
	if client == nil {
		return nil, ErrNilClient
	}
	t := &Tool{client: client}
	typed, err := toolcontract.NewFunc[Request, *Response](
		toolcontract.FuncConfig{Name: toolName, Description: description},
		t.request,
	)
	if err != nil {
		return nil, fmt.Errorf("httpreq: build tool: %w", err)
	}
	t.requestFunc = typed
	return t, nil
}

const description = `Execute a single HTTP request and return the response.
- The "url" must be a fully-formed absolute http(s) URL.
- Method defaults to GET. Write methods (POST/PUT/PATCH/DELETE) only work when configured policy allows them.
- Configured policy restricts which hosts and methods are reachable. A policy rejection is final for that host and method; do not retry the same request.
- Response body is capped by the configured policy; when truncated, response.truncated == true.
- Body and header values contain encoding and data: utf8 is readable text; base64 preserves non-UTF8 bytes.
- For body with JSON content, pass a JSON-encoded string as "body" and set Content-Type via "headers".
- Use this for arbitrary REST/JSON APIs. Prefer the dedicated web_search / web_fetch tools for general web pages.`

func (t *Tool) request(ctx context.Context, request Request) (*Response, error) {
	response, err := t.client.Do(ctx, &request)
	if admission, ok := errors.AsType[*admissionError](err); ok {
		return nil, admissionFailure(admission)
	}
	if err != nil && response != nil {
		return nil, toolresult.WithEvidence("httpreq: request", response, err)
	}
	return response, err
}

// admissionFailure is the definite outcome of a request the client never sent:
// a policy refusal is a rejection and an invalid request a failure.
func admissionFailure(admission *admissionError) error {
	kind := toolcontract.FailureKindFailed
	if errors.Is(admission, ErrHostNotAllowed) || errors.Is(admission, ErrMethodNotAllowed) {
		kind = toolcontract.FailureKindRejected
	}
	cause := fmt.Errorf("httpreq: request: %w", admission)
	failure, err := toolcontract.NewFailure(toolcontract.FailureConfig{
		Kind: kind, Cause: cause, Output: toolcontract.NewTextOutput(cause.Error()),
	})
	if err != nil {
		return errors.Join(cause, err)
	}
	return failure
}
