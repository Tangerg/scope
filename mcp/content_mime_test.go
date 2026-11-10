package mcp_test

import (
	"errors"
	"testing"

	sdk "github.com/Tangerg/go-sdk/mcp"

	"github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/mcp"
)

func TestEmbeddedResourceValidatesProvenanceAtBothBoundaries(t *testing.T) {
	for _, uri := range []string{"relative/path", "not a uri", "file:///bad path", "custom://valid/resource"} {
		for _, mimeType := range []string{"text/plain", "not a mime", ""} {
			for _, blob := range []bool{false, true} {
				resource := &sdk.ResourceContents{URI: uri, MIMEType: mimeType, Text: "source"}
				part := content.Block{Kind: content.Text, Text: "source"}
				textMIME := mimeType
				if blob {
					resource.Text, resource.Blob = "", []byte("source")
					part = content.Block{Kind: content.Media, Media: &media.Media{MIME: "text/plain", Source: media.Source{Kind: media.SourceBytes, Bytes: []byte("source")}}}
					textMIME = ""
				}
				valid := uri == "custom://valid/resource" && mimeType != "not a mime"
				messages, err := mcp.PromptMessagesToChat([]*sdk.PromptMessage{{Role: "user", Content: &sdk.EmbeddedResource{Resource: resource}}})
				if (err == nil) != valid {
					t.Fatalf("import uri=%q MIME=%q blob=%t: %v", uri, mimeType, blob, err)
				}
				part.Metadata, err = metadata.FromValues(map[string]any{mcp.ContentMetadataKey: map[string]any{"type": "resource", "resource": map[string]any{"uri": uri, "mimeType": textMIME}}})
				if err != nil {
					t.Fatal(err)
				}
				_, err = mcp.EncodeToolResult(tool.Output{Content: []content.Block{part}})
				validExport := uri == "custom://valid/resource" && (blob || mimeType != "not a mime")
				if (err == nil) != validExport {
					t.Fatalf("export uri=%q MIME=%q blob=%t: %v", uri, textMIME, blob, err)
				}
				if valid && len(messages) != 1 {
					t.Fatal("valid resource was lost")
				}
			}
		}
	}
}

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
