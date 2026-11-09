// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

//go:generate go run glyph_generate.go

package parser

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

var ErrUnsupportedFontEncoding = errors.New("pdf: unsupported font encoding")

type fontEncoding string

const (
	encodingStandard fontEncoding = "StandardEncoding"
	encodingWinAnsi  fontEncoding = "WinAnsiEncoding"
	encodingMacRoman fontEncoding = "MacRomanEncoding"
	encodingUCS2     fontEncoding = "UniGB-UCS2-H"
)

const (
	fontToUnicodeKey       = "ToUnicode"
	fontEncodingKey        = "Encoding"
	fontBaseEncodingKey    = "BaseEncoding"
	fontDifferencesKey     = "Differences"
	fontDescriptorKey      = "FontDescriptor"
	fontType1ProgramKey    = "FontFile"
	fontTrueTypeProgramKey = "FontFile2"
	fontSubtypeProgramKey  = "FontFile3"
	fontSubtypeKey         = "Subtype"
	fontBaseNameKey        = "BaseFont"
	type1FontSubtype       = "Type1"
)

type standardLatinFont string

const (
	fontTimesRoman           standardLatinFont = "Times-Roman"
	fontTimesBold            standardLatinFont = "Times-Bold"
	fontTimesItalic          standardLatinFont = "Times-Italic"
	fontTimesBoldItalic      standardLatinFont = "Times-BoldItalic"
	fontHelvetica            standardLatinFont = "Helvetica"
	fontHelveticaBold        standardLatinFont = "Helvetica-Bold"
	fontHelveticaOblique     standardLatinFont = "Helvetica-Oblique"
	fontHelveticaBoldOblique standardLatinFont = "Helvetica-BoldOblique"
	fontCourier              standardLatinFont = "Courier"
	fontCourierBold          standardLatinFont = "Courier-Bold"
	fontCourierOblique       standardLatinFont = "Courier-Oblique"
	fontCourierBoldOblique   standardLatinFont = "Courier-BoldOblique"
)

type Font struct {
	V       Value
	decoder TextDecoder
}

func (f *Font) Decoder() TextDecoder {
	if f.decoder == nil {
		f.decoder = f.readDecoder()
	}
	return f.decoder
}

func (f *Font) readDecoder() TextDecoder {
	toUnicode := f.V.Key(fontToUnicodeKey)
	if !toUnicode.IsNull() {
		if toUnicode.Kind() != Stream {
			panic("invalid ToUnicode stream")
		}
		mapping := readCmap(toUnicode)
		if mapping == nil {
			panic("invalid ToUnicode CMap")
		}
		return mapping
	}
	encoding := f.V.Key(fontEncodingKey)
	switch encoding.Kind() {
	case Name:
		if fontEncoding(encoding.Name()) == encodingUCS2 {
			return &utf16Decoder{budget: f.V.r.budget}
		}
		return &byteDecoder{table: f.encodingTable(encoding), budget: f.V.r.budget}
	case Dict:
		decoder := &byteDecoder{table: f.encodingTable(encoding.Key(fontBaseEncodingKey)), budget: f.V.r.budget}
		decoder.applyDifferences(encoding.Key(fontDifferencesKey))
		return decoder
	case Null:
		return &byteDecoder{table: f.builtInEncoding(), budget: f.V.r.budget}
	default:
		panic("invalid font Encoding")
	}
}

func (f *Font) encodingTable(encoding Value) [256]string {
	if encoding.IsNull() {
		return f.builtInEncoding()
	}
	if encoding.Kind() != Name {
		panic("invalid font BaseEncoding")
	}
	switch fontEncoding(encoding.Name()) {
	case encodingStandard:
		return standardEncoding
	case encodingWinAnsi:
		return winAnsiEncoding
	case encodingMacRoman:
		return macRomanEncoding
	default:
		panic(f.unsupportedEncoding(encoding.Name()))
	}
}

func (f *Font) builtInEncoding() [256]string {
	// An embedded font program may replace the standard font's built-in mapping.
	descriptor := f.V.Key(fontDescriptorKey)
	for _, key := range [...]string{fontType1ProgramKey, fontTrueTypeProgramKey, fontSubtypeProgramKey} {
		if !descriptor.Key(key).IsNull() {
			panic(f.unsupportedEncoding("embedded built-in encoding"))
		}
	}
	if f.V.Key(fontSubtypeKey).Name() == type1FontSubtype {
		switch standardLatinFont(f.V.Key(fontBaseNameKey).Name()) {
		case fontTimesRoman, fontTimesBold, fontTimesItalic, fontTimesBoldItalic,
			fontHelvetica, fontHelveticaBold, fontHelveticaOblique, fontHelveticaBoldOblique,
			fontCourier, fontCourierBold, fontCourierOblique, fontCourierBoldOblique:
			return standardEncoding
		}
	}
	panic(f.unsupportedEncoding("unknown built-in encoding"))
}

func (f *Font) unsupportedEncoding(encoding string) error {
	return fmt.Errorf("%w: font %q (%d %d R), encoding %q", ErrUnsupportedFontEncoding, f.V.Key(fontBaseNameKey).Name(), f.V.ptr.id, f.V.ptr.gen, encoding)
}

type TextDecoder interface {
	Decode(raw string) string
}

type utf16Decoder struct {
	budget *budget
}

func (u *utf16Decoder) Decode(raw string) string {
	if len(raw)%2 != 0 {
		panic("invalid UTF-16BE character code")
	}
	u.budget.expand(int64(len(raw)) * utf8.UTFMax)
	return utf16Decode(raw)
}

type byteDecoder struct {
	table  [256]string
	budget *budget
}

func (b *byteDecoder) Decode(raw string) string {
	var text strings.Builder
	for index := range len(raw) {
		sequence := b.table[raw[index]]
		b.budget.expand(int64(len(sequence)))
		text.WriteString(sequence)
	}
	return text.String()
}

func (b *byteDecoder) applyDifferences(differences Value) {
	if differences.IsNull() {
		return
	}
	if differences.Kind() != Array {
		panic("invalid font Differences")
	}
	code := -1
	for index := range differences.Len() {
		differences.r.budget.step(1)
		value := differences.Index(index)
		switch value.Kind() {
		case Integer:
			if value.Int64() < 0 || value.Int64() >= int64(len(b.table)) {
				panic("font Differences code is outside the byte range")
			}
			code = int(value.Int64())
		case Name:
			if code < 0 || code >= len(b.table) {
				panic("font Differences glyph has no byte code")
			}
			sequence, known := glyphUnicode[value.Name()]
			if !known {
				panic(fmt.Errorf("%w: Differences glyph %q", ErrUnsupportedFontEncoding, value.Name()))
			}
			b.table[code] = sequence
			code++
		default:
			panic("invalid font Differences entry")
		}
	}
}
