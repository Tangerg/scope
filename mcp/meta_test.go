package mcp_test

import (
	"encoding/json"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestMetaContextOwnsMapAndValues(t *testing.T) {
	meta := sdkmcp.Meta{"requestId": json.RawMessage(`"original"`)}
	ctx := scopemcp.WithRequestMeta(t.Context(), meta)
	meta["requestId"][1] = 'X'
	meta["requestId"] = json.RawMessage(`"caller mutation"`)

	first := scopemcp.RequestMetaFromContext(ctx)
	if got := first["requestId"]; string(got) != `"original"` {
		t.Fatalf("RequestMetaFromContext requestId = %v, want original", got)
	}
	first["requestId"][1] = 'X'
	first["requestId"] = json.RawMessage(`"consumer mutation"`)

	if got := scopemcp.RequestMetaFromContext(ctx)["requestId"]; string(got) != `"original"` {
		t.Fatalf("second RequestMetaFromContext requestId = %v, want original", got)
	}
}
