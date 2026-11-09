package pdf_test

import (
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

type adobeGlyph struct {
	name string
	text string
}

func readAdobeGlyphs(t *testing.T) []adobeGlyph {
	t.Helper()
	source, err := os.ReadFile("testdata/adobe-glyphlist.txt")
	if err != nil {
		t.Fatal(err)
	}
	var glyphs []adobeGlyph
	for line := range strings.Lines(string(source)) {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, values, found := strings.Cut(line, ";")
		if !found || name == "" {
			t.Fatalf("invalid Adobe Glyph List entry: %q", line)
		}
		var text strings.Builder
		for value := range strings.FieldsSeq(values) {
			scalar, err := strconv.ParseInt(value, 16, 32)
			if err != nil || !utf8.ValidRune(rune(scalar)) {
				t.Fatalf("invalid Adobe glyph scalar %q: %v", value, err)
			}
			text.WriteRune(rune(scalar))
		}
		glyphs = append(glyphs, adobeGlyph{name: name, text: text.String()})
	}
	return glyphs
}

func TestDifferencesPreserveTheCompleteAdobeGlyphList(t *testing.T) {
	glyphs := readAdobeGlyphs(t)
	sequences := 0
	for _, glyph := range glyphs {
		if utf8.RuneCountInString(glyph.text) > 1 {
			sequences++
		}
	}
	if len(glyphs) != 4281 || sequences != 81 {
		t.Fatalf("incomplete Adobe Glyph List fixture: glyphs=%d sequences=%d", len(glyphs), sequences)
	}
	const markerCode = '!'
	const firstGlyphCode = markerCode + 1
	const glyphsPerFont = 256 - firstGlyphCode
	for batch := range slices.Chunk(glyphs, glyphsPerFont) {
		t.Run(batch[0].name, func(t *testing.T) {
			var differences, codes, want strings.Builder
			fmt.Fprintf(&differences, "%d", firstGlyphCode)
			fmt.Fprintf(&codes, "%02x", markerCode)
			want.WriteByte(markerCode)
			for index, glyph := range batch {
				fmt.Fprintf(&differences, " /%s", glyph.name)
				fmt.Fprintf(&codes, "%02x", firstGlyphCode+index)
				want.WriteString(glyph.text)
			}
			fmt.Fprintf(&codes, "%02x", markerCode)
			want.WriteByte(markerCode)
			got := extractPDFText(t, []string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
				"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
				pdfStream("BT /F1 12 Tf <"+codes.String()+"> Tj ET", ""),
				"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding << /BaseEncoding /WinAnsiEncoding /Differences [" + differences.String() + "] >> >>",
			})
			if got != want.String() {
				t.Fatalf("Differences output=%q; Adobe Glyph List requires %q", got, want.String())
			}
		})
	}
}

func TestNamedFontEncodingsFollowPDFGlyphAssignments(t *testing.T) {
	for _, sample := range []struct{ encoding, codes, want string }{
		{"WinAnsiEncoding", "7f818d8f909d", "••••••"},
		{"MacRomanEncoding", "db", "¤"},
		{"StandardEncoding", "2760aed0", "’‘ﬁ—"},
	} {
		t.Run(sample.encoding, func(t *testing.T) {
			got := extractPDFText(t, []string{
				"<< /Type /Catalog /Pages 2 0 R >>",
				"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
				"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> >> /Contents 4 0 R >>",
				pdfStream("BT /F1 12 Tf <"+sample.codes+"> Tj ET", ""),
				"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /" + sample.encoding + " >>",
			})
			if got != sample.want {
				t.Fatalf("text=%q; want %q", got, sample.want)
			}
		})
	}
}
