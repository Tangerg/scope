package mistral

import (
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestToolOutputRejectsMediaInsteadOfDroppingIt(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	message := corechat.NewToolMessage(corechat.ToolResult{
		ID: "call", Name: "inspect", Output: corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: image}}},
	})
	if _, err := mapChatRequestMessages([]corechat.Message{message}); err == nil || !strings.Contains(err.Error(), "media Tool output is unsupported") {
		t.Fatalf("mapChatRequestMessages error = %v", err)
	}
}
