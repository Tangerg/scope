package parser

import (
	"compress/zlib"
	"encoding/ascii85"
	"errors"
	"fmt"
	"io"
	"math"
)

const (
	inlineImageBegin        keyword = "BI"
	inlineImageData         keyword = "ID"
	inlineImageEnd          keyword = "EI"
	jpegMarkerPrefix                = 0xff
	jpegStartOfImage                = 0xd8
	jpegEndOfImage                  = 0xd9
	jpegStartOfScan                 = 0xda
	jpegDefineNumberOfLines         = 0xdc
	jpegFirstRestart                = 0xd0
	jpegLastRestart                 = 0xd7
	jpegTemporaryMarker             = 0x01
	jpegFirstSegmentMarker          = 0xc0
	runLengthEnd                    = 128
	runLengthRepeatBias             = 257
)

const (
	inlineFilterASCIIHex  streamFilter = "AHx"
	inlineFilterASCII85   streamFilter = "A85"
	inlineFilterFlate     streamFilter = "Fl"
	inlineFilterRunLength streamFilter = "RL"
	inlineFilterDCT       streamFilter = "DCT"
)

const (
	colorDeviceGray    = "DeviceGray"
	colorDeviceRGB     = "DeviceRGB"
	colorDeviceCMYK    = "DeviceCMYK"
	colorIndexed       = "Indexed"
	colorCalGray       = "CalGray"
	colorCalRGB        = "CalRGB"
	colorLab           = "Lab"
	colorICCBased      = "ICCBased"
	colorSeparation    = "Separation"
	colorDeviceN       = "DeviceN"
	inlineColorGray    = "G"
	inlineColorRGB     = "RGB"
	inlineColorCMYK    = "CMYK"
	inlineColorIndexed = "I"
)

// Image samples are binary even inside a content stream. Framing follows pixel
// row sizes or the outer filter's terminator, never an EI-shaped byte pattern.
type inlineImage struct {
	input     *buffer
	resources Value
	header    dict
}

func (i *inlineImage) skip() {
	i.header = make(dict)
	for {
		token := i.input.readToken()
		if token == inlineImageData {
			break
		}
		key, ok := token.(name)
		if !ok {
			panic("malformed PDF: inline image requires a name before ID")
		}
		i.header[key] = i.input.readObject()
	}
	separator := i.input.readByte()
	if i.input.eof || !isSpace(separator) {
		panic("malformed PDF: inline image ID requires whitespace")
	}
	source := &inlineImageSource{input: i.input}
	switch filter := i.firstFilter(); filter {
	case "":
		i.skipBytes(source, i.pixelBytes())
	case filterASCIIHex, inlineFilterASCIIHex:
		i.skipASCIIHex(source)
	case filterASCII85, inlineFilterASCII85:
		decoded := ascii85.NewDecoder(newASCII85PayloadReader(source))
		if _, err := io.Copy(io.Discard, &budgetReader{source: decoded, budget: i.input.budget}); err != nil {
			panic(err)
		}
	case filterFlate, inlineFilterFlate:
		if err := i.skipFlate(source); err != nil {
			panic(err)
		}
	case filterRunLength, inlineFilterRunLength:
		i.skipRunLength(source)
	case filterDCT, inlineFilterDCT:
		i.skipJPEG(source)
	default:
		panic(fmt.Errorf("unsupported PDF: inline image filter %q", filter))
	}
	if !isSpace(i.readDataByte(source)) || i.input.readToken() != inlineImageEnd {
		panic("malformed PDF: inline image data not followed by EI")
	}
}

func (i *inlineImage) parameter(full, abbreviated name) object {
	if value, present := i.header[abbreviated]; present {
		return value
	}
	return i.header[full]
}

func (i *inlineImage) firstFilter() streamFilter {
	filter := i.parameter("Filter", "F")
	if filters, ok := filter.(array); ok {
		if len(filters) > maxStreamFilters {
			panic(ErrBudget)
		}
		if len(filters) == 0 {
			return ""
		}
		filter = filters[0]
	}
	if filter == nil {
		return ""
	}
	filterName, ok := filter.(name)
	if !ok {
		panic("malformed PDF: inline image Filter must contain names")
	}
	return streamFilter(filterName)
}

func (i *inlineImage) pixelBytes() int64 {
	width, widthOK := i.parameter("Width", "W").(int64)
	height, heightOK := i.parameter("Height", "H").(int64)
	if !widthOK || !heightOK || width <= 0 || height <= 0 {
		panic("malformed PDF: inline image dimensions must be positive integers")
	}
	bitsValue := i.parameter("BitsPerComponent", "BPC")
	bits, bitsOK := bitsValue.(int64)
	components := int64(1)
	mask := i.parameter("ImageMask", "IM")
	if mask != nil {
		if _, ok := mask.(bool); !ok {
			panic("malformed PDF: inline image mask must be boolean")
		}
	}
	if mask == true {
		if bitsValue != nil && (!bitsOK || bits != 1) {
			panic("malformed PDF: inline image mask requires one bit per component")
		}
		bits, bitsOK = 1, true
	} else {
		components = i.colorComponents(Value{i.resources.r, objptr{}, i.parameter("ColorSpace", "CS")})
	}
	if !bitsOK {
		panic("malformed PDF: inline image requires BitsPerComponent")
	}
	switch bits {
	case 1, 2, 4, 8, 16:
	default:
		panic("malformed PDF: invalid inline image BitsPerComponent")
	}
	if width > (math.MaxInt64-7)/components/bits {
		panic(ErrBudget)
	}
	rowBytes := (width*components*bits + 7) / 8
	if height > math.MaxInt64/rowBytes {
		panic(ErrBudget)
	}
	return rowBytes * height
}

func (i *inlineImage) colorComponents(color Value) int64 {
	i.input.budget.enter()
	defer i.input.budget.leave()
	if color.Kind() == Name {
		switch color.Name() {
		case colorDeviceGray, inlineColorGray:
			return 1
		case colorDeviceRGB, inlineColorRGB:
			return 3
		case colorDeviceCMYK, inlineColorCMYK:
			return 4
		default:
			resource := i.resources.Key("ColorSpace").Key(color.Name())
			if resource.IsNull() {
				panic(fmt.Errorf("malformed PDF: missing inline image color space %q", color.Name()))
			}
			return i.colorComponents(resource)
		}
	}
	if color.Kind() != Array {
		panic("malformed PDF: inline image requires a color space")
	}
	switch color.Index(0).Name() {
	case colorIndexed, inlineColorIndexed, colorCalGray, colorSeparation:
		return 1
	case colorCalRGB, colorLab:
		return 3
	case colorICCBased:
		components := color.Index(1).Key("N").Int64()
		if components == 1 || components == 3 || components == 4 {
			return components
		}
	case colorDeviceN:
		components := color.Index(1)
		if components.Kind() == Array && components.Len() > 0 {
			return int64(components.Len())
		}
	}
	panic("malformed PDF: invalid inline image color space")
}

func (i *inlineImage) readDataByte(source *inlineImageSource) byte {
	value, err := source.ReadByte()
	if err != nil {
		panic(fmt.Errorf("malformed PDF: truncated inline image: %w", err))
	}
	return value
}

func (i *inlineImage) skipBytes(source *inlineImageSource, count int64) {
	if count > i.input.budget.work {
		panic(ErrBudget)
	}
	if _, err := io.CopyN(io.Discard, source, count); err != nil {
		panic(fmt.Errorf("malformed PDF: truncated inline image: %w", err))
	}
}

func (i *inlineImage) skipASCIIHex(source *inlineImageSource) {
	pending := false
	for {
		value := i.readDataByte(source)
		if isSpace(value) {
			continue
		}
		if value == '>' {
			if pending {
				i.input.budget.expand(1)
			}
			return
		}
		if unhex(value) < 0 {
			panic("malformed PDF: invalid ASCIIHex inline image")
		}
		if pending {
			i.input.budget.expand(1)
		}
		pending = !pending
	}
}

func (i *inlineImage) skipFlate(source *inlineImageSource) (err error) {
	decoded, err := zlib.NewReader(source)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, decoded.Close()) }()
	_, err = io.Copy(io.Discard, &budgetReader{source: decoded, budget: i.input.budget})
	return err
}

func (i *inlineImage) skipRunLength(source *inlineImageSource) {
	for {
		length := i.readDataByte(source)
		if length == runLengthEnd {
			return
		}
		if length < runLengthEnd {
			count := int64(length) + 1
			i.input.budget.expand(count)
			i.skipBytes(source, count)
			continue
		}
		i.input.budget.expand(runLengthRepeatBias - int64(length))
		i.readDataByte(source)
	}
}

func (i *inlineImage) skipJPEG(source *inlineImageSource) {
	if i.readDataByte(source) != jpegMarkerPrefix || i.readDataByte(source) != jpegStartOfImage {
		panic("malformed PDF: inline JPEG missing SOI")
	}
	entropy := false
	for {
		value := i.readDataByte(source)
		if entropy && value != jpegMarkerPrefix {
			continue
		}
		if value != jpegMarkerPrefix {
			panic("malformed PDF: inline JPEG missing marker")
		}
		marker := i.readDataByte(source)
		for marker == jpegMarkerPrefix {
			marker = i.readDataByte(source)
		}
		if entropy && marker == 0 {
			continue
		}
		if marker == jpegEndOfImage {
			return
		}
		if marker == jpegTemporaryMarker {
			continue
		}
		if marker >= jpegFirstRestart && marker <= jpegLastRestart {
			if !entropy {
				panic("malformed PDF: inline JPEG restart outside scan")
			}
			continue
		}
		if marker < jpegFirstSegmentMarker || marker == jpegStartOfImage {
			panic("malformed PDF: invalid inline JPEG marker")
		}
		length := int64(i.readDataByte(source))<<8 | int64(i.readDataByte(source))
		if length < 2 {
			panic("malformed PDF: invalid inline JPEG segment length")
		}
		i.skipBytes(source, length-2)
		if marker != jpegDefineNumberOfLines {
			entropy = marker == jpegStartOfScan
		}
	}
}

// Byte-sized reads prevent filter buffering from consuming the following PDF
// operator. ByteReader also gives zlib an exact boundary at its own checksum.
type inlineImageSource struct{ input *buffer }

func (i *inlineImageSource) Read(data []byte) (int, error) {
	if len(data) == 0 {
		return 0, nil
	}
	value, err := i.ReadByte()
	if err != nil {
		return 0, err
	}
	data[0] = value
	return 1, nil
}

func (i *inlineImageSource) ReadByte() (byte, error) {
	value := i.input.readByte()
	if i.input.eof {
		return 0, io.EOF
	}
	return value, nil
}
