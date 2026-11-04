package mcp_test

import (
	"errors"
	"testing"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp"
)

func TestCoreMIMEChangeSurvivesMCPProjection(t *testing.T) {
	for name, source := range map[string]sdk.Content{
		"link":     &sdk.ResourceLink{URI: "https://example.com/asset", Name: "asset"},
		"embedded": &sdk.EmbeddedResource{Resource: &sdk.ResourceContents{URI: "file:///asset", Blob: []byte{1}}},
	} {
		t.Run(name, func(t *testing.T) {
			messages, err := mcp.PromptMessagesToChat([]*sdk.PromptMessage{{Role: "user", Content: source}})
			if err != nil || len(messages) != 1 {
				t.Fatalf("initial decode: %+v %v", messages, err)
			}
			part, ok := messages[0].Parts[0].Content()
			if !ok || part.Media.MIME != "" {
				t.Fatal("unexpected initial media", part)
			}
			for _, mimeType := range []string{"image/png", "application/pdf", ""} {
				part.Media.MIME = mimeType
				if err := part.Validate(); err != nil {
					t.Fatal("Core MIME declaration was not valid", err)
				}
				out, err := mcp.EncodeToolResult(tool.Output{Content: []content.Block{part}})
				if err != nil {
					t.Fatal(err)
				}
				back, err := mcp.PromptMessagesToChat([]*sdk.PromptMessage{{Role: "user", Content: out.Content[0]}})
				if err != nil {
					t.Fatal(err)
				}
				if got := back[0].Parts[0].Media.MIME; got != part.Media.MIME {
					t.Fatalf("valid current Core MIME=%q was erased by retained private MCP metadata=%s; round-trip MIME=%q", part.Media.MIME, part.Metadata[mcp.ContentMetadataKey], got)
				}
			}
		})
	}
}

func TestImageAndAudioContentRequireDeclaredMIME(t *testing.T) {
	for _, source := range []sdk.Content{
		&sdk.ImageContent{Data: []byte{1}},
		&sdk.AudioContent{Data: []byte{1}},
	} {
		if _, err := mcp.PromptMessagesToChat([]*sdk.PromptMessage{{Role: "user", Content: source}}); !errors.Is(err, media.ErrInvalidMIME) {
			t.Fatalf("undeclared %T MIME was accepted: %v", source, err)
		}
	}
	for _, source := range []sdk.Content{
		&sdk.ImageContent{MIMEType: "image/png", Data: []byte{1}},
		&sdk.AudioContent{MIMEType: "audio/wav", Data: []byte{1}},
	} {
		messages, err := mcp.PromptMessagesToChat([]*sdk.PromptMessage{{Role: "user", Content: source}})
		if err != nil {
			t.Fatal(err)
		}
		part, _ := messages[0].Parts[0].Content()
		part.Media.MIME = ""
		if _, err := mcp.EncodeToolResult(tool.Output{Content: []content.Block{part}}); !errors.Is(err, media.ErrInvalidMIME) {
			t.Fatalf("%T preserved its type after losing required MIME: %v", source, err)
		}
	}
}
