package mcp_test

import (
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	scopemcp "github.com/Tangerg/scope/mcp"
)

func TestMetaContextOwnsTopLevelMap(t *testing.T) {
	meta := sdkmcp.Meta{"requestId": "original"}
	ctx := scopemcp.WithRequestMeta(t.Context(), meta)
	meta["requestId"] = "caller mutation"

	first := scopemcp.RequestMetaFromContext(ctx)
	if got := first["requestId"]; got != "original" {
		t.Fatalf("RequestMetaFromContext requestId = %v, want original", got)
	}
	first["requestId"] = "consumer mutation"

	if got := scopemcp.RequestMetaFromContext(ctx)["requestId"]; got != "original" {
		t.Fatalf("second RequestMetaFromContext requestId = %v, want original", got)
	}
}
