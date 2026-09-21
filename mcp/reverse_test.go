package mcp_test

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/chat"
	toolcontract "github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp"
)

type progressTool struct{}

func (progressTool) Definition() chat.ToolDefinition {
	return chat.ToolDefinition{
		Name:        "progress_demo",
		Description: "Reports progress + log lines",
		InputSchema: json.RawMessage(`{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func (progressTool) Call(ctx context.Context, _ toolcontract.Invocation) (chat.ToolOutput, error) {
	total := 3.0
	for i := range 3 {
		if err := mcp.ReportProgress(ctx, float64(i+1), &total, "step"); err != nil &&
			!errors.Is(err, mcp.ErrNoServerSession) {
			return chat.ToolOutput{}, err
		}
	}
	return chat.NewTextToolOutput("ok"), nil
}

func TestNotifyHelpers_Progress(t *testing.T) {
	ctx := t.Context()

	var mu sync.Mutex
	var progress []sdkmcp.ProgressNotificationParams

	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "test-srv"}, nil)
	require.NoError(t, mcp.Register(server, progressTool{}))

	cli := sdkmcp.NewClient(
		&sdkmcp.Implementation{Name: "test-cli"},
		&sdkmcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *sdkmcp.ProgressNotificationClientRequest) {
				mu.Lock()
				progress = append(progress, *req.Params)
				mu.Unlock()
			},
		},
	)

	srvT, cliT := sdkmcp.NewInMemoryTransports()
	srvSession, err := server.Connect(ctx, srvT, nil)
	require.NoError(t, err)
	defer srvSession.Close()

	cliSession, err := cli.Connect(ctx, cliT, nil)
	require.NoError(t, err)
	defer cliSession.Close()

	// Issue a tools/call with a progress token so notifications flow.
	params := &sdkmcp.CallToolParams{Name: "progress_demo"}
	params.SetProgressToken("p1")
	res, err := cliSession.CallTool(ctx, params)
	require.NoError(t, err)
	assert.False(t, res.IsError, "tool result: %#v", res)

	// Give the SDK a moment to deliver notifications (they go through
	// a goroutine on the client side). Close the sessions, which
	// flushes pending notifications.
	require.NoError(t, cliSession.Close())
	require.NoError(t, srvSession.Wait())

	mu.Lock()
	defer mu.Unlock()
	assert.Len(t, progress, 3, "expected 3 progress notifications")
}

func TestNotifyHelpers_NoSessionReturnsSentinel(t *testing.T) {
	ctx := t.Context()
	err := mcp.ReportProgress(ctx, 1, nil, "no session")
	assert.ErrorIs(t, err, mcp.ErrNoServerSession)
	_, err = mcp.Elicit(ctx, sdkmcp.ElicitParams{Message: "x"})
	assert.ErrorIs(t, err, mcp.ErrNoServerSession)
}
