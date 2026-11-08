package speech

import (
	"errors"
	"io"
	"iter"

	"github.com/samber/lo"
)

const readChunkSize = 16 * 1024

// ReadChunks transfers independently owned bytes in read order, before any
// non-EOF error from the same read. EOF ends iteration without an error.
// Reading starts on iteration and stops when the consumer stops; the caller
// retains responsibility for closing the reader and governing blocked reads.
// A nil reader, including a typed nil, yields one error without reading.
func ReadChunks(reader io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		if lo.IsNil(reader) {
			yield(nil, errors.New("speech: nil reader"))
			return
		}
		for {
			buffer := make([]byte, readChunkSize)
			read, err := reader.Read(buffer)
			if read > 0 && !yield(buffer[:read], nil) {
				return
			}
			if err != nil {
				if err != io.EOF {
					yield(nil, err)
				}
				return
			}
		}
	}
}
