package mcp_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/go-sdk/jsonrpc"
	"github.com/stretchr/testify/require"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestMCPPublishesOnlyOneCompleteInvocationFailure(t *testing.T) {
	failure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New("internal cause"), Output: chat.NewTextToolOutput("complete failure"),
	})
	require.NoError(t, err)
	otherFailure, err := tool.NewFailure(tool.FailureConfig{
		Kind: tool.FailureKindFailed, Cause: errors.New("another cause"), Output: chat.NewTextToolOutput("other failure"),
	})
	require.NoError(t, err)
	for _, test := range []struct {
		name     string
		cause    error
		definite bool
	}{
		{name: "complete", cause: failure, definite: true},
		{name: "transparent wrapping", cause: fmt.Errorf("context: %w", failure), definite: true},
		{name: "one joined cause", cause: errors.Join(failure), definite: true},
		{name: "unknown sibling", cause: errors.Join(failure, errors.New("uncertain external operation"))},
		{name: "unknown preceding cause", cause: errors.Join(errors.New("uncertain external operation"), failure)},
		{name: "independent failure", cause: errors.Join(failure, otherFailure)},
	} {
		t.Run(test.name, func(t *testing.T) {
			executable, err := tool.NewFunc(tool.FuncConfig{Name: "execute"}, func(context.Context, struct{}) (string, error) {
				return "", test.cause
			})
			require.NoError(t, err)
			session, cleanup := connectPair(t, t.Context(), executable)
			defer cleanup()
			remote, err := scopemcp.DiscoverTools(t.Context(), []scopemcp.ToolSource{{Session: session}}, scopemcp.ToolDiscoveryConfig{})
			require.NoError(t, err)
			require.Len(t, remote, 1)
			result, err := invokeTestTool(t.Context(), remote[0], `{}`)
			require.Error(t, err)
			require.Empty(t, result.Content)
			require.Empty(t, result.Details)
			observed, definite := tool.ClassifyFailure(err)
			require.Equal(t, test.definite, definite)
			if test.definite {
				text, textOnly := observed.Output().Text()
				require.True(t, textOnly)
				require.Equal(t, "complete failure", text)
				return
			}
			protocolError, found := errors.AsType[*jsonrpc.Error](err)
			require.True(t, found)
			require.Equal(t, int64(jsonrpc.CodeInternalError), protocolError.Code)
			require.Empty(t, protocolError.Data)
			require.NotContains(t, err.Error(), "complete failure")
			require.NotContains(t, err.Error(), "uncertain external operation")
		})
	}
}
