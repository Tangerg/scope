package bedrock

import (
	"encoding/json"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime/types"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestToolResultContentPreservesJSONTextAndMedia(t *testing.T) {
	structured, err := mapToolResultContent(corechat.ToolOutput{Details: json.RawMessage(`{"count":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if len(structured) != 1 {
		t.Fatalf("structured content = %#v", structured)
	}
	if _, ok := structured[0].(*types.ToolResultContentBlockMemberJson); !ok {
		t.Fatalf("structured block = %#v", structured[0])
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	document, err := media.NewBytes("application/pdf", []byte("pdf"))
	if err != nil {
		t.Fatal(err)
	}
	document.Name = "evidence.pdf"
	content, err := mapToolResultContent(corechat.ToolOutput{Content: []corechat.ToolContent{
		{Kind: corechat.PartText, Text: "summary"}, {Kind: corechat.PartMedia, Media: image}, {Kind: corechat.PartMedia, Media: document},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(content) != 3 {
		t.Fatalf("content = %#v", content)
	}
	if _, ok := content[0].(*types.ToolResultContentBlockMemberText); !ok {
		t.Fatalf("text block = %#v", content[0])
	}
	if _, ok := content[1].(*types.ToolResultContentBlockMemberImage); !ok {
		t.Fatalf("image block = %#v", content[1])
	}
	if _, ok := content[2].(*types.ToolResultContentBlockMemberDocument); !ok {
		t.Fatalf("document block = %#v", content[2])
	}
}

func TestToolResultContentRejectsUnsupportedAudio(t *testing.T) {
	audio, err := media.NewBytes("audio/wav", []byte("audio"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = mapToolResultContent(corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: audio}}})
	if err == nil {
		t.Fatal("mapToolResultContent accepted audio")
	}
}
