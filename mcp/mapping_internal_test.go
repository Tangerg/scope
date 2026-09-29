package mcp

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"net/url"
	"strings"
	"testing"

	sdkmcp "github.com/Tangerg/go-sdk/mcp"

	corechat "github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/media"
)

const pngMIME = "image/png"

func mediaPart(t *testing.T, part corechat.ToolContent) *media.Media {
	t.Helper()
	if part.Kind != corechat.PartMedia || part.Media == nil {
		t.Fatalf("part = %#v, want media", part)
	}
	return part.Media
}

func TestMapRemoteContentCoversEveryProtocolShape(t *testing.T) {
	cases := map[string]struct {
		content sdkmcp.Content
		include bool
		assert  func(t *testing.T, part corechat.ToolContent)
	}{
		"text": {
			content: &sdkmcp.TextContent{Text: "hello"},
			include: true,
			assert: func(t *testing.T, part corechat.ToolContent) {
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
			assert: func(t *testing.T, part corechat.ToolContent) {
				if mediaPart(t, part).MIME != pngMIME {
					t.Fatalf("image MIME = %q", part.Media.MIME)
				}
			},
		},
		"audio": {
			content: &sdkmcp.AudioContent{MIMEType: "audio/mpeg", Data: []byte("\xFF\xFB")},
			include: true,
			assert: func(t *testing.T, part corechat.ToolContent) {
				if mediaPart(t, part).MIME != "audio/mpeg" {
					t.Fatalf("audio MIME = %q", part.Media.MIME)
				}
			},
		},
		"resource link": {
			content: &sdkmcp.ResourceLink{MIMEType: pngMIME, URI: "https://example.com/a.png", Name: "diagram"},
			include: true,
			assert: func(t *testing.T, part corechat.ToolContent) {
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
			assert: func(t *testing.T, part corechat.ToolContent) {
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
			assert: func(t *testing.T, part corechat.ToolContent) {
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
			assert: func(t *testing.T, part corechat.ToolContent) {
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

func TestMapServerContentRejectsAnInvalidProtocolEnvelope(t *testing.T) {
	for _, raw := range []string{`{}`, `{"type":"unknown"}`, `{"type":"resource_link"}`, `{"type":"text","resource":{"uri":"file:///source"}}`} {
		part := corechat.ToolContent{Kind: corechat.PartText, Text: "body"}
		if err := part.Metadata.Set(ContentMetadataKey, json.RawMessage(raw)); err != nil {
			t.Fatal(err)
		}
		if _, err := mapServerContent(part); err == nil {
			t.Fatalf("accepted invalid content envelope %s", raw)
		}
	}
}

func TestMapRemoteContentRejectsUnusableResources(t *testing.T) {
	for _, content := range []sdkmcp.Content{
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{}},
		&sdkmcp.EmbeddedResource{},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{MIMEType: "not a mime", URI: "https://example.com/a"}},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{Text: "text", Blob: []byte("blob")}},
	} {
		if _, _, err := mapRemoteContent(content); err == nil {
			t.Fatalf("unusable resource was admitted: %#v", content)
		}
	}
	part, include, err := mapRemoteContent(&sdkmcp.ResourceLink{URI: "https://example.com/a"})
	if err != nil || !include || part.Kind != corechat.PartMedia || part.Media.MIME != "application/octet-stream" {
		t.Fatalf("resource without MIME = %#v, %v, %v", part, include, err)
	}
}

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
		Content: []corechat.ToolContent{
			{Kind: corechat.PartText, Text: "summary"},
			{Kind: corechat.PartMedia, Media: image},
			{Kind: corechat.PartMedia, Media: audio},
			{Kind: corechat.PartMedia, Media: opaque},
			{Kind: corechat.PartMedia, Media: linked},
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
	unsupported := corechat.ToolOutput{Content: []corechat.ToolContent{{Kind: corechat.PartKind("reasoning")}}}
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
			data, err := jsonv2.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var wire map[string]json.RawMessage
			if decodeErr := jsonv2.Unmarshal(data, &wire); decodeErr != nil {
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
	data, err := jsonv2.Marshal(empty)
	if err != nil {
		t.Fatal(err)
	}
	var wire map[string]json.RawMessage
	if decodeErr := jsonv2.Unmarshal(data, &wire); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if _, present := wire["structuredContent"]; present {
		t.Fatalf("absent details acquired structuredContent: %s", data)
	}
}

func TestMapServerMediaRejectsAnUnusableSource(t *testing.T) {
	if _, err := mapServerMedia(&media.Media{MIME: "not a mime"}, nil); err == nil {
		t.Fatal("an unparsable MIME type was accepted")
	}
	if _, err := mapServerMedia(&media.Media{MIME: pngMIME}, nil); err == nil {
		t.Fatal("an unset media source was accepted")
	}
}

func TestMapServerMediaCarriesReferences(t *testing.T) {
	reference, err := media.NewReference(pngMIME, "store://bucket/key")
	if err != nil {
		t.Fatal(err)
	}
	content, err := mapServerMedia(reference, nil)
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

func TestMapServerMediaEscapesInlineResourceNames(t *testing.T) {
	opaque, err := media.NewBytes("application/pdf", []byte("%PDF"))
	if err != nil {
		t.Fatal(err)
	}
	opaque.Name = "quarterly report#1?.pdf"
	content, err := mapServerMedia(opaque, nil)
	if err != nil {
		t.Fatal(err)
	}
	embedded, ok := content.(*sdkmcp.EmbeddedResource)
	if !ok {
		t.Fatalf("content = %T, want EmbeddedResource", content)
	}
	const want = "scope://tool-output/quarterly%20report%231%3F.pdf"
	if embedded.Resource.URI != want {
		t.Fatalf("embedded resource URI = %q, want %q", embedded.Resource.URI, want)
	}
	parsed, err := url.Parse(embedded.Resource.URI)
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Path != "/"+opaque.Name || parsed.Fragment != "" || parsed.RawQuery != "" {
		t.Fatalf("parsed URI = %#v, want the name as its only path segment", parsed)
	}
}
