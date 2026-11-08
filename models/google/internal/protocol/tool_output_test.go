package protocol

import (
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"testing"

	corecontent "github.com/Tangerg/scope/core/content"
	"github.com/Tangerg/scope/core/media"
	toolcontract "github.com/Tangerg/scope/core/tool"
)

func TestProtocolToolResultPreservesStructuredAndMediaContent(t *testing.T) {
	response, parts, err := protocolToolResult(toolcontract.Output{Details: json.RawMessage(`{"value":4}`)}, false)
	if err != nil {
		t.Fatal(err)
	}
	encoded, encodeErr := jsonv2.Marshal(response)
	if encodeErr != nil {
		t.Fatal(encodeErr)
	}
	if string(encoded) != `{"output":{"value":4}}` || len(parts) != 0 {
		t.Fatalf("structured result = %#v, %#v", response, parts)
	}

	image, err := media.NewBytes("image/png", []byte("image"))
	if err != nil {
		t.Fatal(err)
	}
	linked, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	response, parts, err = protocolToolResult(toolcontract.Output{Content: []corecontent.Block{
		{Kind: corecontent.Text, Text: "caption"}, {Kind: corecontent.Media, Media: image}, {Kind: corecontent.Media, Media: linked},
	}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if response["output"] != "caption" || len(parts) != 2 || parts[0].InlineData == nil || parts[0].InlineData.MIMEType != "image/png" {
		t.Fatalf("multimodal result = %#v, %#v", response, parts)
	}
	if parts[1].FileData == nil || parts[1].FileData.MIMEType != "image/png" || parts[1].FileData.FileURI != linked.Source.URI {
		t.Fatalf("linked media result = %#v", parts[1])
	}
}

func TestProtocolToolResultRejectsUnsupportedReferenceMedia(t *testing.T) {
	reference, err := media.NewReference("image/png", "provider-file")
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = protocolToolResult(toolcontract.Output{Content: []corecontent.Block{{Kind: corecontent.Media, Media: reference}}}, false)
	if err == nil {
		t.Fatal("protocolToolResult accepted provider reference media")
	}
}

func TestProtocolToolResultPreservesJSONTypes(t *testing.T) {
	for _, test := range []struct {
		value string
		want  string
	}{
		{value: `[1,2]`, want: `[1,2]`},
		{value: `9007199254740993`, want: `9007199254740993`},
		{value: `true`, want: `true`},
		{value: `null`, want: `null`},
		{value: `"quoted"`, want: `"quoted"`},
		{value: `plain text`, want: `"plain text"`},
	} {
		t.Run(test.value, func(t *testing.T) {
			for _, isError := range []bool{false, true} {
				key := "output"
				if isError {
					key = "error"
				}
				encoded, err := jsonv2.Marshal(protocolToolResultJSON([]byte(test.value), isError))
				if err != nil {
					t.Fatal(err)
				}
				want := `{"` + key + `":` + test.want + `}`
				if string(encoded) != want {
					t.Fatalf("encoded result = %s, want %s", encoded, want)
				}
			}
		})
	}
}
