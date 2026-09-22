package media_test

import (
	"bytes"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/Tangerg/scope/core/media"
	"github.com/Tangerg/scope/core/metadata"
)

func TestConstructors(t *testing.T) {
	tests := []struct {
		name string
		new  func() (*media.Media, error)
		kind media.SourceKind
	}{
		{name: "bytes", new: func() (*media.Media, error) { return media.NewBytes("image/png", []byte("png")) }, kind: media.SourceBytes},
		{name: "URI", new: func() (*media.Media, error) { return media.NewURI("image/png", "https://example.com/image.png") }, kind: media.SourceURI},
		{name: "reference", new: func() (*media.Media, error) { return media.NewReference("image/png", "provider-file-1") }, kind: media.SourceReference},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.new()
			if err != nil {
				t.Fatalf("constructor: %v", err)
			}
			if got.Source.Kind != tt.kind {
				t.Fatalf("kind = %q, want %q", got.Source.Kind, tt.kind)
			}
			if got.Metadata == nil {
				t.Fatal("Metadata must be initialized")
			}
		})
	}
}

func TestNewBytesCopiesInputAndOutput(t *testing.T) {
	input := []byte("payload")
	m, err := media.NewBytes("application/octet-stream", input)
	if err != nil {
		t.Fatal(err)
	}
	input[0] = 'X'

	got, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	got[0] = 'Y'
	second, err := m.Bytes()
	if err != nil {
		t.Fatal(err)
	}
	if string(second) != "payload" {
		t.Fatalf("stored bytes = %q, want payload", second)
	}
}

func TestMediaClone(t *testing.T) {
	value, err := media.NewBytes("image/png", []byte("payload"))
	if err != nil {
		t.Fatal(err)
	}
	if err := value.Metadata.Set("source", "caller"); err != nil {
		t.Fatal(err)
	}

	clone := value.Clone()
	clone.Source.Bytes[0] = 'X'
	clone.Metadata["source"][1] = 'X'

	if got := string(value.Source.Bytes); got != "payload" {
		t.Fatalf("source bytes = %q, want payload", got)
	}
	if got := string(value.Metadata["source"]); got != `"caller"` {
		t.Fatalf("source metadata = %s, want caller", got)
	}

	var nilMedia *media.Media
	if nilMedia.Clone() != nil {
		t.Fatal("nil Media.Clone must return nil")
	}
}

func TestConstructorsRejectInvalidInputs(t *testing.T) {
	tests := []struct {
		name string
		new  func() (*media.Media, error)
		want error
	}{
		{name: "empty MIME", new: func() (*media.Media, error) { return media.NewBytes("", []byte("x")) }, want: media.ErrInvalidMIME},
		{name: "malformed MIME", new: func() (*media.Media, error) { return media.NewBytes("image", []byte("x")) }, want: media.ErrInvalidMIME},
		{name: "empty bytes", new: func() (*media.Media, error) { return media.NewBytes("image/png", nil) }, want: media.ErrInvalidSource},
		{name: "relative URI", new: func() (*media.Media, error) { return media.NewURI("image/png", "/image.png") }, want: media.ErrInvalidSource},
		{name: "empty URI resource", new: func() (*media.Media, error) { return media.NewURI("image/png", "https://") }, want: media.ErrInvalidSource},
		{name: "blank reference", new: func() (*media.Media, error) { return media.NewReference("image/png", "  ") }, want: media.ErrInvalidSource},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := tt.new()
			if !errors.Is(err, tt.want) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, tt.want)
			}
		})
	}
}

func TestMediaSourceValidateRejectsAmbiguousAndUnknownSources(t *testing.T) {
	tests := []media.Source{
		{},
		{Kind: "future", Ref: "value"},
		{Kind: media.SourceBytes, Bytes: []byte("x"), URI: "https://example.com"},
		{Kind: media.SourceURI, URI: "https://example.com", Ref: "also-set"},
		{Kind: media.SourceReference, Bytes: []byte("x"), Ref: "ref"},
	}
	for _, source := range tests {
		if err := source.Validate(); !errors.Is(err, media.ErrInvalidSource) {
			t.Errorf("Validate(%+v) error = %v, want ErrInvalidSource", source, err)
		}
	}
}

func TestAccessorsRejectWrongKind(t *testing.T) {
	m, err := media.NewURI("image/png", "https://example.com/image.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Bytes(); !errors.Is(err, media.ErrInvalidSource) {
		t.Fatalf("Bytes error = %v, want ErrInvalidSource", err)
	}
	if got, err := m.URI(); err != nil || got != "https://example.com/image.png" {
		t.Fatalf("URI = (%q, %v)", got, err)
	}
	if _, err := m.Reference(); !errors.Is(err, media.ErrInvalidSource) {
		t.Fatalf("Reference error = %v, want ErrInvalidSource", err)
	}
}

func TestReferenceAccessor(t *testing.T) {
	m, err := media.NewReference("application/pdf", "file-123")
	if err != nil {
		t.Fatal(err)
	}
	if got, err := m.Reference(); err != nil || got != "file-123" {
		t.Fatalf("Reference = (%q, %v)", got, err)
	}
}

func TestNilAccessors(t *testing.T) {
	var m *media.Media
	if _, err := m.Bytes(); !errors.Is(err, media.ErrNilMedia) {
		t.Fatalf("Bytes error = %v, want ErrNilMedia", err)
	}
	if _, err := m.URI(); !errors.Is(err, media.ErrNilMedia) {
		t.Fatalf("URI error = %v, want ErrNilMedia", err)
	}
	if _, err := m.Reference(); !errors.Is(err, media.ErrNilMedia) {
		t.Fatalf("Reference error = %v, want ErrNilMedia", err)
	}
	if err := m.Validate(); !errors.Is(err, media.ErrNilMedia) {
		t.Fatalf("Validate error = %v, want ErrNilMedia", err)
	}
}

func TestJSONRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		new  func() (*media.Media, error)
	}{
		{name: "bytes", new: func() (*media.Media, error) { return media.NewBytes("image/png", []byte{1, 2, 3, 255}) }},
		{name: "URI", new: func() (*media.Media, error) { return media.NewURI("image/png", "data:image/png;base64,AQID") }},
		{name: "reference", new: func() (*media.Media, error) { return media.NewReference("image/png", "file-123") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, err := tt.new()
			if err != nil {
				t.Fatal(err)
			}
			src.ID = "media-1"
			src.Name = "image.png"
			if setErr := src.Metadata.Set("width", 64); setErr != nil {
				t.Fatal(setErr)
			}

			encoded, err := jsonv2.Marshal(src)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			if !strings.Contains(string(encoded), `"kind":"`+string(src.Source.Kind)+`"`) {
				t.Fatalf("missing source discriminator: %s", encoded)
			}

			var got media.Media
			if err := jsonv2.Unmarshal(encoded, &got); err != nil {
				t.Fatalf("Unmarshal: %v", err)
			}
			if got.MIME != src.MIME || got.ID != src.ID || got.Name != src.Name || got.Source.Kind != src.Source.Kind {
				t.Fatalf("round trip = %+v, want %+v", got, *src)
			}
			if !bytes.Equal(got.Source.Bytes, src.Source.Bytes) || got.Source.URI != src.Source.URI || got.Source.Ref != src.Source.Ref {
				t.Fatalf("round-trip source = %+v, want %+v", got.Source, src.Source)
			}
		})
	}
}

func TestJSONRejectsInvalidValues(t *testing.T) {
	tests := []string{
		`{"mime":"image/png","source":{"kind":"future","ref":"x"}}`,
		`{"mime":"image/png","source":{"kind":"bytes","bytes":"%%%"}}`,
		`{"mime":"image/png","source":{"kind":"uri","uri":"relative.png"}}`,
	}
	for _, input := range tests {
		var got media.Media
		if err := jsonv2.Unmarshal([]byte(input), &got); err == nil {
			t.Errorf("Unmarshal accepted %s", input)
		}
	}
}

func TestValidateRecursesIntoMetadata(t *testing.T) {
	m, err := media.NewBytes("image/png", []byte("png"))
	if err != nil {
		t.Fatal(err)
	}
	m.Metadata["bad"] = json.RawMessage(`{`)
	if err := m.Validate(); !errors.Is(err, metadata.ErrInvalidValue) {
		t.Fatalf("Validate error = %v, want metadata.ErrInvalidValue", err)
	}
	if _, err := jsonv2.Marshal(m); !errors.Is(err, metadata.ErrInvalidValue) {
		t.Fatalf("Marshal error = %v, want metadata.ErrInvalidValue", err)
	}
}
