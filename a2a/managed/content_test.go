package managed

import (
	"errors"
	"testing"

	sdk "github.com/a2aproject/a2a-go/v2/a2a"
)

func TestJSONContentDoesNotRoundAnAgentAnswer(t *testing.T) {
	raw := sdk.NewRawPart([]byte(`{"amount":9007199254740993}`))
	raw.MediaType = "application/json"
	for _, part := range []*sdk.Part{raw, sdk.NewDataPart(map[string]any{"enabled": true}), sdk.NewDataPart(map[string]any{"risk": float64(9007199254740992)})} {
		out, err := input(sdk.NewMessage(sdk.MessageRoleUser, part))
		if v, ok := part.Content.(sdk.Data); ok {
			if _, has := v.Value.(map[string]any)["risk"]; has {
				if !errors.Is(err, sdk.ErrUnsupportedContentType) {
					t.Fatal("rounded SDK value accepted", err)
				}
				continue
			}
		}
		if err != nil {
			t.Fatal(err)
		}
		if part == raw && string(out.JSON()) != `{"amount":9007199254740993}` {
			t.Fatalf("lost integer: %s", out.JSON())
		}
	}
}
