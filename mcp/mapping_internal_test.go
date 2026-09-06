package mcp

import (
	"encoding/json"
	"strings"
	"testing"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const pngMIME = "image/png"

func mediaPart(t *testing.T, part corechat.Part) *media.Media {
	t.Helper()
	if part.Kind != corechat.PartMedia || part.Media == nil {
		t.Fatalf("part = %#v, want media", part)
	}
	return part.Media
}

// TestMapRemoteContentCoversEveryProtocolShape pins the inbound half of the
// adapter. Every branch here is a distinct MCP content type, and a shape that
// silently falls through to the JSON fallback would hand the model an encoded
// envelope instead of the resource the server sent.
func TestMapRemoteContentCoversEveryProtocolShape(t *testing.T) {
	cases := map[string]struct {
		content sdkmcp.Content
		include bool
		assert  func(t *testing.T, part corechat.Part)
	}{
		"text": {
			content: &sdkmcp.TextContent{Text: "hello"},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if part.Kind != corechat.PartText || part.Text != "hello" {
					t.Fatalf("part = %#v", part)
				}
			},
		},
		"empty text is dropped": {
			content: &sdkmcp.TextContent{},
		},
		"image": {
			content: &sdkmcp.ImageContent{MIMEType: pngMIME, Data: []byte("\x89PNG")},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if mediaPart(t, part).MIME != pngMIME {
					t.Fatalf("image MIME = %q", part.Media.MIME)
				}
			},
		},
		"audio": {
			content: &sdkmcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("\xFF\xFB")},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if mediaPart(t, part).MIME != "audio/mpeg" {
					t.Fatalf("audio MIME = %q", part.Media.MIME)
				}
			},
		},
		"resource link": {
			content: &sdkmcp.ResourceLink{MIMEType: pngMIME, URI: "https://example.com/a.png", Name: "diagram"},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				linked := mediaPart(t, part)
				if linked.Name != "diagram" {
					t.Fatalf("resource link name = %q", linked.Name)
				}
				uri, err := linked.URI()
				if err != nil || uri != "https://example.com/a.png" {
					t.Fatalf("resource link URI = %q, %v", uri, err)
				}
			},
		},
		"embedded text resource": {
			content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{Text: "inline"}},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if part.Text != "inline" {
					t.Fatalf("embedded text = %q", part.Text)
				}
			},
		},
		"embedded blob resource": {
			content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
				MIMEType: pngMIME,
				Blob:     []byte("\x89PNG"),
			}},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if mediaPart(t, part).MIME != pngMIME {
					t.Fatalf("embedded blob MIME = %q", part.Media.MIME)
				}
			},
		},
		"embedded linked resource": {
			content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
				MIMEType: pngMIME,
				URI:      "https://example.com/b.png",
			}},
			include: true,
			assert: func(t *testing.T, part corechat.Part) {
				if mediaPart(t, part).MIME != pngMIME {
					t.Fatalf("embedded link MIME = %q", part.Media.MIME)
				}
			},
		},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			part, include, err := mapRemoteContent(testCase.content)
			if err != nil {
				t.Fatal(err)
			}
			if include != testCase.include {
				t.Fatalf("include = %t, want %t", include, testCase.include)
			}
			if include {
				testCase.assert(t, part)
			}
		})
	}
}

// TestMapRemoteContentFallsBackToEncodedJSON documents the deliberate escape
// hatch: an unknown or unusable shape is preserved verbatim as text rather than
// dropped, so a caller can still see what the server sent.
func TestMapRemoteContentFallsBackToEncodedJSON(t *testing.T) {
	cases := map[string]sdkmcp.Content{
		"resource link without a MIME type": &sdkmcp.ResourceLink{URI: "https://example.com/a"},
		"embedded resource with no payload": &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{}},
		"nil embedded resource":             &sdkmcp.EmbeddedResource{},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			part, include, err := mapRemoteContent(content)
			if err != nil {
				t.Fatal(err)
			}
			if !include || part.Kind != corechat.PartText || part.Text == "" {
				t.Fatalf("part = %#v, include = %t", part, include)
			}
		})
	}
}

func TestMapEmbeddedResourceSkipsUnusableResources(t *testing.T) {
	cases := map[string]*sdkmcp.ResourceContents{
		"nil":                nil,
		"empty":              {},
		"URI without a MIME": {URI: "https://example.com/a"},
		"MIME without a URI": {MIMEType: pngMIME},
		"unparsable MIME":    {MIMEType: "not a mime", URI: "https://example.com/a"},
	}
	for name, resource := range cases {
		t.Run(name, func(t *testing.T) {
			part, include, err := mapEmbeddedResource(resource)
			if err != nil {
				t.Fatal(err)
			}
			if include {
				t.Fatalf("unusable resource was included as %#v", part)
			}
		})
	}
}

// TestMapServerToolOutputCoversEveryPartKind pins the outbound half: what a
// Scope Tool returns has to arrive at an MCP client as the matching content
// type, and structured details must land in StructuredContent rather than text.
func TestMapServerToolOutputCoversEveryPartKind(t *testing.T) {
	image, err := media.NewBytes(pngMIME, []byte("\x89PNG"))
	if err != nil {
		t.Fatal(err)
	}
	audio, err := media.NewBytes("audio/mpeg", []byte("\xFF\xFB"))
	if err != nil {
		t.Fatal(err)
	}
	opaque, err := media.NewBytes("application/pdf", []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	opaque.Name = "report"
	linked, err := media.NewURI(pngMIME, "https://example.com/a.png")
	if err != nil {
		t.Fatal(err)
	}

	output := corechat.ToolOutput{
		Content: []corechat.Part{
			corechat.NewTextPart("summary"),
			corechat.NewMediaPart(image),
			corechat.NewMediaPart(audio),
			corechat.NewMediaPart(opaque),
			corechat.NewMediaPart(linked),
		},
		Details: []byte(`{"score":1}`),
	}

	result, err := mapServerToolOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Content) != 5 {
		t.Fatalf("content length = %d, want 5", len(result.Content))
	}
	if _, ok := result.Content[0].(*sdkmcp.TextContent); !ok {
		t.Fatalf("content[0] = %T, want TextContent", result.Content[0])
	}
	if _, ok := result.Content[1].(*sdkmcp.ImageContent); !ok {
		t.Fatalf("content[1] = %T, want ImageContent", result.Content[1])
	}
	if _, ok := result.Content[2].(*sdkmcp.AudioContent); !ok {
		t.Fatalf("content[2] = %T, want AudioContent", result.Content[2])
	}
	embedded, ok := result.Content[3].(*sdkmcp.EmbeddedResource)
	if !ok {
		t.Fatalf("content[3] = %T, want EmbeddedResource", result.Content[3])
	}
	if !strings.HasSuffix(embedded.Resource.URI, "report") {
		t.Fatalf("embedded resource URI = %q", embedded.Resource.URI)
	}
	if result.StructuredContent == nil {
		t.Fatal("structured details did not reach StructuredContent")
	}
}

func TestMapServerToolOutputRejectsUnusableOutput(t *testing.T) {
	if _, err := mapServerToolOutput(corechat.ToolOutput{Details: []byte(`{`)}); err == nil {
		t.Fatal("invalid details were accepted")
	}
	unsupported := corechat.ToolOutput{Content: []corechat.Part{{Kind: corechat.PartKind("reasoning")}}}
	if _, err := mapServerToolOutput(unsupported); err == nil {
		t.Fatal("an unsupported part kind was accepted")
	}
}

func TestMapServerToolOutputPreservesStructuredJSON(t *testing.T) {
	for _, details := range []string{
		`{"id":9007199254740993,"precise":0.12345678901234567890123456789}`,
		`[9007199254740993,0.12345678901234567890123456789]`,
		`9007199254740993`,
		`0.12345678901234567890123456789`,
		`null`,
	} {
		t.Run(details, func(t *testing.T) {
			output, err := corechat.NewJSONToolOutput(json.RawMessage(details))
			if err != nil {
				t.Fatal(err)
			}
			result, err := mapServerToolOutput(output)
			if err != nil {
				t.Fatal(err)
			}
			clear(output.Details)
			data, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if decodeErr := json.Unmarshal(data, &wire); decodeErr != nil {
				t.Fatal(decodeErr)
			}
			if got := string(wire["structuredContent"]); got != details {
				t.Fatalf("structuredContent=%s, want %s", got, details)
			}
		})
	}
	empty, err := mapServerToolOutput(corechat.ToolOutput{})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if decodeErr := json.Unmarshal(data, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if _, present := wire["structuredContent"]; present {
		t.Fatalf("absent details acquired structuredContent: %s", data)
	}
}

func TestMapServerMediaRejectsAnUnusableSource(t *testing.T) {
	if _, err := mapServerMedia(&media.Media{MIME: "not a mime"}); err == nil {
		t.Fatal("an unparsable MIME type was accepted")
	}
	if _, err := mapServerMedia(&media.Media{MIME: pngMIME}); err == nil {
		t.Fatal("an unset media source was accepted")
	}
}

// TestMapServerMediaCarriesReferences keeps a by-reference payload addressable:
// collapsing it into an empty resource would lose the only handle the client
// has.
func TestMapServerMediaCarriesReferences(t *testing.T) {
	reference, err := media.NewReference(pngMIME, "store://bucket/key")
	if err != nil {
		t.Fatal(err)
	}
	content, err := mapServerMedia(reference)
	if err != nil {
		t.Fatal(err)
	}
	link, ok := content.(*sdkmcp.ResourceLink)
	if !ok {
		if embedded, embeddedOK := content.(*sdkmcp.EmbeddedResource); embeddedOK {
			if embedded.Resource.URI != "store://bucket/key" {
				t.Fatalf("embedded reference URI = %q", embedded.Resource.URI)
			}
			return
		}
		t.Fatalf("content = %T", content)
	}
	if link.URI != "store://bucket/key" {
		t.Fatalf("resource link URI = %q", link.URI)
	}
}

// TestPromptContentToPartCoversEveryProtocolShape mirrors the tool-result
// mapping for prompts, which share the content vocabulary but not the code
// path.
func TestPromptContentToPartCoversEveryProtocolShape(t *testing.T) {
	cases := map[string]struct {
		content sdkmcp.Content
		include bool
	}{
		"text":                  {content: &sdkmcp.TextContent{Text: "hello"}, include: true},
		"empty text is dropped": {content: &sdkmcp.TextContent{}},
		"image":                 {content: &sdkmcp.ImageContent{MIMEType: pngMIME, Data: []byte("\x89PNG")}, include: true},
		"audio":                 {content: &sdkmcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("\xFF")}, include: true},
		"resource link":         {content: &sdkmcp.ResourceLink{MIMEType: pngMIME, URI: "https://example.com/a.png"}, include: true},
		"embedded text":         {content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{Text: "inline"}}, include: true},
		"embedded blob":         {content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{MIMEType: pngMIME, Blob: []byte("\x89PNG")}}, include: true},
		"embedded linked resource": {content: &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{
			MIMEType: pngMIME,
			URI:      "https://example.com/b.png",
		}}, include: true},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			part, include, err := promptContentToPart(testCase.content)
			if err != nil {
				t.Fatal(err)
			}
			if include != testCase.include {
				t.Fatalf("include = %t, want %t (part %#v)", include, testCase.include, part)
			}
		})
	}
}

func TestPromptContentToPartRejectsUnusableContent(t *testing.T) {
	cases := map[string]sdkmcp.Content{
		"nil text":          (*sdkmcp.TextContent)(nil),
		"nil image":         (*sdkmcp.ImageContent)(nil),
		"nil audio":         (*sdkmcp.AudioContent)(nil),
		"nil resource link": (*sdkmcp.ResourceLink)(nil),
		"nil embedded":      (*sdkmcp.EmbeddedResource)(nil),
		"empty embedded":    &sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{}},
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := promptContentToPart(content); err == nil {
				t.Fatal("unusable prompt content was accepted")
			}
		})
	}
}
