package mistral

import (
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestToolOutputRejectsMediaInsteadOfDroppingIt(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	message := corechat.NewToolMessage(corechat.ToolResult{
		ID: "call", Name: "inspect", Output: toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: image}}},
	})
	if _, err := mapChatRequestMessages([]corechat.Message{message}); err == nil || !strings.Contains(err.Error(), "media Tool output is unsupported") {
		t.Fatalf("mapChatRequestMessages error = %v", err)
	}
}
