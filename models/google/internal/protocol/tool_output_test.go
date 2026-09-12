package protocol

import (
	"encoding/json"
	"testing"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

func TestProtocolToolResultPreservesStructuredAndMediaContent(t *testing.T) {
	response, parts, err := protocolToolResult(corechat.ToolOutput{Details: json.RawMessage(`{"value":4}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if response["value"] != float64(4) || len(parts) != 0 {
		t.Fatalf("structured result = %#v, %#v", response, parts)
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	response, parts, err = protocolToolResult(corechat.ToolOutput{Content: []corechat.ToolContent{
		{Kind: corechat.PartText, Text: "caption"}, {Kind: corechat.PartMedia, Media: image},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if response["output"] != "caption" || len(parts) != 1 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != "image/png" {
		t.Fatalf("multimodal result = %#v, %#v", response, parts)
	}
}

func TestProtocolToolResultRejectsUnsupportedReferenceMedia(t *testing.T) {
	reference, err := media.NewReference("image/png", "provider-file")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = protocolToolResult(corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartMedia, Media: reference}}}, false)
	if err == nil {
		t.Fatal("protocolToolResult accepted provider reference media")
	}
}
