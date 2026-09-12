package anthropic

import (
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestToolResultPreservesTextImageAndDocumentContent(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := media.NewBytes("application/pdf", []byte("pdf"))
	if err != nil {
		t.Fatal(err)
	}
	document.Name = "evidence.pdf"
	mapped, err := mapProtocolToolResult(corechat.ToolResult{
		ID: "call", Name: "inspect", IsError: true,
		Output: corechat.ToolOutput{Content: []corechat.ToolContent{
			{Kind: corechat.PartText, Text: "failed"}, {Kind: corechat.PartMedia, Media: image}, {Kind: corechat.PartMedia, Media: document},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	result := mapped.OfToolResult
	if result == nil || !result.IsError.Valid() || !result.IsError.Value || len(result.Content) != 3 ||
		result.Content[0].OfText == nil || result.Content[1].OfImage == nil || result.Content[2].OfDocument == nil {
		t.Fatalf("mapped result = %#v", mapped)
	}
}

func TestToolResultRejectsUnsupportedMedia(t *testing.T) {
	audio, err := media.NewBytes("audio/wav", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mapProtocolToolResult(corechat.ToolResult{
		ID: "call", Name: "listen", Output: corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: audio}}},
	})
	if err == nil {
		t.Fatal("mapProtocolToolResult accepted unsupported audio")
	}
}
