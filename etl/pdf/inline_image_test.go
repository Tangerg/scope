package pdf_test

import (
	"bytes"
	"compress/zlib"
	"encoding/ascii85"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strings"
	"testing"

	"github.com/Tangerg/scope/etl/pdf"
)

func TestNativeInlineImageAndHybridReferenceFixtures(t *testing.T) {
	for _, fixture := range []struct{ name, want string }{
		{"inline_phantom.pdf", "VISIBLE_TEXT"},
		{"inline_lost_page.pdf", "VISIBLE_TEXT"},
		{"hybrid_updated_font.pdf", "€"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			data, err := os.ReadFile("testdata/" + fixture.name)
			if err != nil {
				t.Fatal(err)
			}
			reader, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)), pdf.ReaderConfig{})
			if err != nil {
				t.Fatal(err)
			}
			docs, err := reader.Read(t.Context())
			if err != nil || len(docs) != 1 || docs[0].Text != fixture.want {
				t.Fatalf("documents=%v error=%v; want %q", docs, err, fixture.want)
			}
		})
	}
}

func inlineImageObjects(program string) []string {
	return []string{
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Count 1 /Kids [3 0 R] >>",
		"<< /Type /Page /Parent 2 0 R /Resources << /Font << /F1 5 0 R >> /ColorSpace << /Palette [/Indexed /DeviceRGB 1 <000000ffffff>] >> >> /Contents 4 0 R >>",
		pdfStream(program, ""),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>",
	}
}

func TestInlineImagesUsePixelAndFilterBoundaries(t *testing.T) {
	pixels := []byte(" EI\nBT /F1 12 Tf (PHANTOM) Tj ET\n")
	for len(pixels)%3 != 0 {
		pixels = append(pixels, ' ')
	}
	var compressed bytes.Buffer
	compressor := zlib.NewWriter(&compressed)
	if _, err := compressor.Write(pixels); err != nil {
		t.Fatal(err)
	}
	if err := compressor.Close(); err != nil {
		t.Fatal(err)
	}
	ascii := make([]byte, ascii85.MaxEncodedLen(len(pixels)))
	ascii = append(ascii[:ascii85.Encode(ascii, pixels)], '~', '>')
	var photo bytes.Buffer
	if err := jpeg.Encode(&photo, image.NewGray(image.Rect(0, 0, 1, 1)), nil); err != nil {
		t.Fatal(err)
	}
	jpegData := append([]byte{0xff, 0xd8, 0xff, 0xe1, 0, byte(len(pixels) + 2)}, pixels...)
	jpegData = append(jpegData, photo.Bytes()[2:]...)
	width := len(pixels) / 3
	for _, sample := range []struct {
		name, header string
		data         []byte
	}{
		{"raw RGB with EI and text-shaped pixels", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB", width), pixels},
		{"raw leading whitespace", "/W 1 /H 1 /BPC 8 /CS /RGB", []byte(" \n\t")},
		{"per-row bit padding", "/W 1 /H 2 /IM true", []byte{0x80, 0}},
		{"color resource", "/W 1 /H 1 /BPC 8 /CS /Palette", []byte{1}},
		{"abbreviated width takes precedence", "/Width 99 /W 1 /H 1 /BPC 8 /CS /G", []byte("Q")},
		{"ASCIIHex", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB /F /AHx", width), []byte(" \n" + hex.EncodeToString(pixels) + ">")},
		{"ASCII85", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB /F /A85", width), append([]byte(" \n"), ascii...)},
		{"Flate", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB /Filter /FlateDecode", width), compressed.Bytes()},
		{"filter array", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB /F [/A85]", width), ascii},
		{"RunLength", fmt.Sprintf("/W %d /H 1 /BPC 8 /CS /RGB /F /RL", width), append(append([]byte{byte(len(pixels) - 1)}, pixels...), 128)},
		{"JPEG markers inside application segment", "/W 1 /H 1 /BPC 8 /CS /G /F /DCT", jpegData},
	} {
		t.Run(sample.name, func(t *testing.T) {
			program := "q BI " + sample.header + " ID\n" + string(sample.data) + "\nEI Q BT /F1 12 Tf (VISIBLE) Tj ET"
			if text := extractPDFText(t, inlineImageObjects(program)); text != "VISIBLE" {
				t.Fatalf("text=%q; want VISIBLE", text)
			}
		})
	}
}

func TestInlineImageDataCanCrossContentStreams(t *testing.T) {
	objects := inlineImageObjects("")
	objects[2] = strings.Replace(objects[2], "/Contents 4 0 R", "/Contents [4 0 R 6 0 R]", 1)
	objects[3] = pdfStream("BI /W 1 /H 1 /BPC 8 /CS /RGB ID\n)", "")
	objects = append(objects, pdfStream("<<\nEI BT /F1 12 Tf (VISIBLE) Tj ET", ""))
	if text := extractPDFText(t, objects); text != "VISIBLE" {
		t.Fatalf("text=%q", text)
	}
}

func TestMalformedInlineImagesCannotPublishPageText(t *testing.T) {
	for _, program := range []string{
		"ID", "EI", "BI /W 1 BI", "BI /W 1", "BI /W 1 /H 1 /BPC 8 /CS /RGB ID\n",
		"BI /W 2 /H 1 /BPC 8 /CS /RGB ID\n)<<\nEI", "BI /W 1 /H 1 /BPC 8 /CS /RGB ID\n)<<\nQ",
		"BI /W -1 /H 1 /BPC 8 /CS /RGB ID\n)<<\nEI", "BI /W 1 /H 1 /BPC 3 /CS /RGB ID\n)<<\nEI",
		"BI /W 1 /H 1 /BPC 8 /CS /Missing ID\n)<<\nEI", "BI /W 1 /H 1 /IM /true ID\nx\nEI",
		"BI /W 1 /H 1 /F /AHx ID\nzz>\nEI", "BI /W 1 /H 1 /F /A85 ID\n~\nEI",
		"BI /W 1 /H 1 /F /Fl ID\nnot-zlib\nEI", "BI /W 1 /H 1 /F /DCT ID\nnot-jpeg\nEI",
		"BI /W 1 /H 1 /F /Unknown ID\nbytes\nEI",
	} {
		payload := encodePDFObjects(inlineImageObjects("BT /F1 12 Tf (BEFORE) Tj ET " + program))
		reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{})
		if err != nil {
			t.Fatal(err)
		}
		if docs, err := reader.Read(t.Context()); len(docs) != 0 || !errors.Is(err, pdf.ErrPartialRead) {
			t.Fatalf("program=%q documents=%v error=%v", program, docs, err)
		}
	}
}

func TestInlineImageExpansionUsesTheReadBudget(t *testing.T) {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	if _, err := writer.Write(bytes.Repeat([]byte{0}, 1<<20)); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	program := "BI /W 1024 /H 1024 /BPC 8 /CS /G /F /Fl ID\n" + compressed.String() + "\nEI"
	assertParseBudget(t, encodePDFObjects(inlineImageObjects(program)), pdf.ParseBudget{MaxDecodedBytes: 4096})
}
