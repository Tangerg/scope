package mcp_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/Tangerg/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestAuthorizationOutcomeSurvivesMCPBoundary(t *testing.T) {
	inner, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindRejected, Output: chat.NewTextToolOutput("private dependency output"),
		Cause: errors.New("private policy detail"),
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name  string
		cause error
	}{
		{name: "refused"},
		{name: "authorization failed", cause: errors.New("private policy detail")},
		{name: "nested refused dependency", cause: inner},
		{name: "canceled refused dependency", cause: errors.Join(context.DeadlineExceeded, inner)},
	} {
		t.Run(test.name, func(t *testing.T) {
			var executions atomic.Int32
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "inspect"}, func(context.Context, struct{}) (string, error) {
				executions.Add(1)
				return "executed", nil
			})
			require.NoError(t, err)
			guard, err := tool.NewGuard(tool.GuardConfig{
				Tool: executable, Authorizer: tool.AuthorizerFunc(func(context.Context, tool.Authorization) (bool, error) {
					return false, test.cause
				}),
			})
			require.NoError(t, err)
			session, cleanup := connectPair(t, t.Context(), guard)
			defer cleanup()
			remote, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
			require.NoError(t, err)
			require.Len(t, remote, 1)
			_, err = invokeTestTool(t.Context(), remote[0], `{}`)
			require.Error(t, err)
			require.Zero(t, executions.Load())
			require.NotContains(t, err.Error(), "private")
			failure, found := errors.AsType[*tool.Failure](err)
			if test.cause == nil {
				require.True(t, found)
				require.Equal(t, tool.FailureKindFailed, failure.Kind())
				text, textOnly := failure.Output().Text()
				require.True(t, textOnly)
				require.Equal(t, `error: tool "inspect" is not authorized`, text)
			} else {
				require.False(t, found, "undecided authorization became a definite Tool result")
				protocolError, found := errors.AsType[*jsonrpc.Error](err)
				require.True(t, found)
				require.Equal(t, int64(jsonrpc.CodeInternalError), protocolError.Code)
				require.Empty(t, protocolError.Data)
			}
		})
	}
}

func TestInvalidFailureCannotBecomeRemoteDefiniteOutcome(t *testing.T) {
	for _, invalid := range []*tool.Failure{nil, {}} {
		executable, err := tool.NewFunc(tool.FuncConfig{Name: "invalid"}, func(context.Context, struct{}) (string, error) {
			return "", invalid
		})
		require.NoError(t, err)
		session, cleanup := connectPair(t, t.Context(), executable)
		remote, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
		require.NoError(t, err)
		require.Len(t, remote, 1)
		_, err = invokeTestTool(t.Context(), remote[0], `{}`)
		_, definite := errors.AsType[*tool.Failure](err)
		require.False(t, definite)
		protocolError, found := errors.AsType[*jsonrpc.Error](err)
		require.True(t, found, "call error = %v", err)
		require.Equal(t, int64(jsonrpc.CodeInternalError), protocolError.Code)
		cleanup()
	}
}
