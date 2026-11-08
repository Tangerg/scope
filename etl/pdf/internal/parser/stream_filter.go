// Copyright 2014 The Go Authors.  All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package parser

import (
	"compress/zlib"
	"encoding/ascii85"
	"fmt"
	"io"
)

// Deep filter chains allocate a decoder and add recursive Read frames for each
// stage before their output reaches the byte budget.
const maxStreamFilters = 16

type streamFilter string

const (
	filterASCIIHex  streamFilter = "ASCIIHexDecode"
	filterASCII85   streamFilter = "ASCII85Decode"
	filterFlate     streamFilter = "FlateDecode"
	filterRunLength streamFilter = "RunLengthDecode"
	filterDCT       streamFilter = "DCTDecode"
)

func (s streamFilter) decode(rd io.Reader, param Value, grant *budget, closers *[]io.Closer) io.Reader {
	if !param.IsNull() && param.Kind() != Dict {
		panic("malformed PDF: DecodeParms must be a dictionary")
	}
	switch s {
	case filterFlate:
		zr, err := zlib.NewReader(rd)
		if err != nil {
			panic(err)
		}
		*closers = append(*closers, zr)
		decoded := &budgetReader{source: zr, budget: grant}
		switch predictor := positiveDecodeParameter(param, "Predictor", predictorNone); predictor {
		case predictorNone:
			return decoded
		case predictorPNGUp:
			return newPNGUpReader(decoded, param, grant)
		default:
			panic(fmt.Errorf("unsupported PDF predictor %d", predictor))
		}
	case filterASCII85:
		if len(param.Keys()) != 0 {
			panic("unsupported DecodeParms for ASCII85")
		}
		return &budgetReader{source: ascii85.NewDecoder(newASCII85PayloadReader(rd)), budget: grant}
	default:
		panic("unknown filter " + string(s))
	}
}
