package anthropic

import (
	"encoding/json"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
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
	mapper := protocolMessageMapper{}
	mapped, err := mapper.mapToolResult(corechat.ToolResult{
		ID: "call", Name: "inspect", IsError: true,
		Output: toolcontract.Output{Content: []corecontent.Block{
			{Kind: corecontent.Text, Text: "failed"}, {Kind: corecontent.Media, Media: image}, {Kind: corecontent.Media, Media: document},
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
	mapper := protocolMessageMapper{}
	_, err = mapper.mapToolResult(corechat.ToolResult{
		ID: "call", Name: "listen", Output: toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: audio}}},
	})
	if err == nil {
		t.Fatal("tool result accepted unsupported audio")
	}
}

func TestToolCallWithBlankArgumentsReplaysAsTheEmptyObject(t *testing.T) {
	for _, arguments := range []string{"", " \n\t"} {
		block := mapProtocolToolCall(corechat.ToolCall{ID: "call-1", Name: "lookup", Arguments: arguments})
		if input, ok := block.OfToolUse.Input.(json.RawMessage); !ok || string(input) != "{}" {
			t.Fatalf("arguments %q replayed as %#v", arguments, block.OfToolUse.Input)
		}
	}
}
