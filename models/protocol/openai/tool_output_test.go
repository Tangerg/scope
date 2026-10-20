package openai

import (
	"encoding/json"
	"strings"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestResponsesToolOutputPreservesStructuredAndMultimodalContent(t *testing.T) {
	structured, err := mapResponsesToolOutput(corechat.ToolOutput{Details: json.RawMessage(`{"count":2}`)})
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
	mapped, err := mapResponsesToolOutput(corechat.ToolOutput{Content: []corechat.ToolContent{
		{Kind: corechat.PartText, Text: "summary"}, {Kind: corechat.PartMedia, Media: image}, {Kind: corechat.PartMedia, Media: file},
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
		ID: "call", Name: "inspect", Output: corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: image}}},
	})
	if _, err := mapRequestMessage(message); err == nil || !strings.Contains(err.Error(), "does not support media Tool output") {
		t.Fatalf("mapRequestMessage error = %v", err)
	}
}
