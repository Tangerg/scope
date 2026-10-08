package mcp

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
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

func TestMapServerContentRejectsMediaReferences(t *testing.T) {
	reference, err := media.NewReference(pngMIME, "store://bucket/key")
	if err != nil {
		t.Fatal(err)
	}
	if content, err := mapServerContent(corechat.ToolContent{Kind: corechat.PartMedia, Media: reference}); !errors.Is(err, ErrMediaReference) || content != nil {
		t.Fatalf("reference media = %#v, %v; want ErrMediaReference", content, err)
	}
}

func TestRemoteContentRejectsMediaFieldsOnText(t *testing.T) {
	for _, encoded := range []string{`{"name":"x"}`, `{"media_id":"x"}`, `{"media_metadata":{"k":1}}`} {
		text := &sdkmcp.TextContent{Text: "hello", Meta: sdkmcp.Meta{coreContentMetadataKey: encoded}}
		if part, _, err := mapRemoteContent(text); err == nil {
			t.Fatalf("text with %s = %#v, want an error", encoded, part)
		}
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

func TestResourceMIMEInfersOnlyFromTheFixedTable(t *testing.T) {
	for uri, want := range map[string]string{
		"https://example.com/a.png":         "image/png",
		"https://example.com/a.JPG":         "image/jpeg",
		"https://example.com/a.jpeg?x=.gif": "image/jpeg",
		"file:///repo/a.gif":                "image/gif",
		"file:///repo/a.webp":               "image/webp",
		"file:///repo/a.mp3":                "audio/mpeg",
		"file:///repo/a.wav":                "audio/wav",
		"file:///repo/a.pdf":                "application/pdf",
		"file:///repo/a.txt":                "text/plain",
		"file:///repo/a.html":               "application/octet-stream",
		"file:///repo/a.md":                 "application/octet-stream",
		"file:///repo/a":                    "application/octet-stream",
		"%zz":                               "application/octet-stream",
	} {
		if got := resourceMIME("", uri); got != want {
			t.Errorf("resourceMIME(%q) = %q, want %q", uri, got, want)
		}
	}
	if got := resourceMIME("text/html", "file:///repo/a.png"); got != "text/html" {
		t.Fatalf("declared MIME = %q, want text/html", got)
	}
}

func TestMapServerContentRejectsAMisplacedInferredMIME(t *testing.T) {
	image, err := media.NewBytes(pngMIME, []byte("\x89PNG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		part     corechat.ToolContent
		envelope string
	}{
		{part: corechat.ToolContent{Kind: corechat.PartText, Text: "body"}, envelope: `{"type":"text","mimeTypeInferred":true}`},
		{part: corechat.ToolContent{Kind: corechat.PartText, Text: "body"}, envelope: `{"type":"resource","resource":{"uri":"file:///a"},"mimeTypeInferred":true}`},
		{part: corechat.ToolContent{Kind: corechat.PartMedia, Media: image}, envelope: `{"type":"image","mimeTypeInferred":true}`},
	} {
		part := test.part
		if err := part.Metadata.Set(ContentMetadataKey, json.RawMessage(test.envelope)); err != nil {
			t.Fatal(err)
		}
		if _, err := mapServerContent(part); err == nil {
			t.Fatalf("accepted inferred MIME in %s", test.envelope)
		}
	}
}

func TestMapServerContentOmitsAnInferredMIME(t *testing.T) {
	linked, err := media.NewURI(pngMIME, "https://example.com/a.png")
	if err != nil {
		t.Fatal(err)
	}
	blob, err := media.NewBytes(pngMIME, []byte("\x89PNG"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		media    *media.Media
		envelope string
		want     string
	}{
		{media: linked, envelope: `{"type":"resource_link","mimeTypeInferred":true}`, want: `{"type":"resource_link","uri":"https://example.com/a.png"}`},
		{media: blob, envelope: `{"type":"resource","resource":{"uri":"file:///a.png"},"mimeTypeInferred":true}`, want: `{"type":"resource","resource":{"uri":"file:///a.png","blob":"iVBORw=="}}`},
	} {
		part := corechat.ToolContent{Kind: corechat.PartMedia, Media: test.media}
		if err := part.Metadata.Set(ContentMetadataKey, json.RawMessage(test.envelope)); err != nil {
			t.Fatal(err)
		}
		content, err := mapServerContent(part)
		if err != nil {
			t.Fatal(err)
		}
		got, err := jsonv2.Marshal(content)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != test.want {
			t.Fatalf("content = %s, want %s", got, test.want)
		}
	}
}

func TestMapRemoteContentRejectsAMisplacedCoreName(t *testing.T) {
	meta := sdkmcp.Meta{coreContentMetadataKey: json.RawMessage(`"{\"name\":\"x\"}"`)}
	for _, content := range []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "body", Meta: meta},
		&sdkmcp.EmbeddedResource{Resource: &sdkmcp.ResourceContents{URI: "file:///a", Text: "body"}, Meta: meta},
		&sdkmcp.ResourceLink{URI: "https://example.com/a.png", Name: "a", MIMEType: pngMIME, Meta: meta},
	} {
		if _, _, err := mapRemoteContent(content); err == nil {
			t.Fatalf("accepted a Core name on %T", content)
		}
	}
	part, _, err := mapRemoteContent(&sdkmcp.ImageContent{MIMEType: pngMIME, Data: []byte("\x89PNG"), Meta: meta})
	if err != nil {
		t.Fatal(err)
	}
	if mediaPart(t, part).Name != "x" {
		t.Fatalf("image name = %q, want x", part.Media.Name)
	}
}
