package parser

import (
	"fmt"
	"io"
	"math"
)

const (
	predictorNone  = 1
	predictorPNGUp = 12
	pngFilterUp    = 2
	bitsPerByte    = 8
)

type pngUpReader struct {
	r    io.Reader
	hist []byte
	tmp  []byte
	pend []byte
}

func newPNGUpReader(source io.Reader, parameters Value, grant *budget) *pngUpReader {
	columns := positiveDecodeParameter(parameters, "Columns", 1)
	colors := positiveDecodeParameter(parameters, "Colors", 1)
	bits := positiveDecodeParameter(parameters, "BitsPerComponent", bitsPerByte)
	switch bits {
	case 1, 2, 4, 8, 16:
	default:
		panic(fmt.Errorf("unsupported PNG component width %d", bits))
	}
	if columns > math.MaxInt64/colors || columns*colors > (math.MaxInt64-bitsPerByte+1)/bits {
		panic(ErrBudget)
	}
	rowBytes := (columns*colors*bits + bitsPerByte - 1) / bitsPerByte
	if rowBytes >= grant.decoded/2 {
		panic(ErrBudget)
	}
	grant.expand(2 * (rowBytes + 1))
	return &pngUpReader{r: source, hist: make([]byte, rowBytes+1), tmp: make([]byte, rowBytes+1)}
}

func (p *pngUpReader) Read(data []byte) (int, error) {
	n := 0
	for len(data) > 0 {
		if len(p.pend) > 0 {
			written := copy(data, p.pend)
			n += written
			data = data[written:]
			p.pend = p.pend[written:]
			continue
		}
		if _, err := io.ReadFull(p.r, p.tmp); err != nil {
			return n, err
		}
		if p.tmp[0] != pngFilterUp {
			return n, fmt.Errorf("malformed PNG-Up encoding")
		}
		for index := 1; index < len(p.tmp); index++ {
			p.hist[index] += p.tmp[index]
		}
		p.pend = p.hist[1:]
	}
	return n, nil
}

func positiveDecodeParameter(parameters Value, key string, fallback int64) int64 {
	value := parameters.Key(key)
	if value.IsNull() {
		return fallback
	}
	if value.Kind() != Integer || value.Int64() <= 0 {
		panic(fmt.Errorf("malformed PDF: invalid %s", key))
	}
	return value.Int64()
}
