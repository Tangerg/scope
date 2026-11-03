package openai

import (
	"encoding/json"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestResponsesToolOutputPreservesStructuredAndMultimodalContent(t *testing.T) {
	structured, err := mapResponsesToolOutput(toolcontract.Output{Details: json.RawMessage(`{"count":2}`)})
	if err != nil {
		t.Fatal(err)
	}
	if !structured.OfString.Valid() || structured.OfString.Value != `{"count":2}` {
		t.Fatalf("structured output = %#v", structured)
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := media.NewReference("application/pdf", "file-id")
	if err != nil {
		t.Fatal(err)
	}
	file.Name = "evidence.pdf"
	mapped, err := mapResponsesToolOutput(toolcontract.Output{Content: []corecontent.Block{
		{Kind: corecontent.Text, Text: "summary"}, {Kind: corecontent.Media, Media: image}, {Kind: corecontent.Media, Media: file},
	}})
	if err != nil {
		t.Fatal(err)
	}
	items := mapped.OfResponseFunctionCallOutputItemArray
	if len(items) != 3 || items[0].OfInputText == nil || items[1].OfInputImage == nil || items[2].OfInputFile == nil {
		t.Fatalf("mapped output = %#v", mapped)
	}
	mappedImage := items[1].OfInputImage
	if mappedImage.FileID.Valid() || !mappedImage.ImageURL.Valid() || !strings.HasPrefix(mappedImage.ImageURL.Value, "data:image/png;base64,") {
		t.Fatalf("mapped image = %#v", mappedImage)
	}
	mappedFile := items[2].OfInputFile
	if !mappedFile.FileID.Valid() || mappedFile.FileID.Value != "file-id" || mappedFile.FileURL.Valid() || mappedFile.FileData.Valid() {
		t.Fatalf("mapped file = %#v", mappedFile)
	}
}

func TestOpenAIChatToolOutputRejectsMediaInsteadOfDroppingIt(t *testing.T) {
	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	message := corechat.NewToolMessage(corechat.ToolResult{
		ID: "call", Name: "inspect", Output: toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: image}}},
	})
	if _, err := mapRequestMessage(message); err == nil || !strings.Contains(err.Error(), "does not support media Tool output") {
		t.Fatalf("mapRequestMessage error = %v", err)
	}
}
