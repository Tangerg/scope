package elevenlabs

import (
	"io"
	"iter"
)

const streamReadBufferSize = 16 * 1024

func readAudioChunks(reader io.Reader) iter.Seq2[[]byte, error] {
	return func(yield func([]byte, error) bool) {
		for {
			buffer := make([]byte, streamReadBufferSize)
			read, err := reader.Read(buffer)
			// Reader may return both bytes and an error; deliver the bytes first.
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
