package speech

import (
	"fmt"
	"iter"
)

const DefaultMaxAudioBytes int64 = 32 << 20

// Collect publishes a complete result only after the stream succeeds. It
// retains the terminal chunk's metadata without mutating borrowed responses.
func Collect(stream iter.Seq2[*Response, error], maxAudioBytes int64) (*Response, error) {
	if stream == nil || maxAudioBytes <= 0 {
		return nil, fmt.Errorf("%w: collection requires a stream and positive audio byte limit", ErrInvalidOptions)
	}
	var audio []byte
	var last *Response
	for response, err := range stream {
		if err != nil {
			return nil, err
		}
		if err := response.Validate(); err != nil {
			return nil, err
		}
		if int64(len(response.Output.Audio)) > maxAudioBytes-int64(len(audio)) {
			return nil, fmt.Errorf("%w: audio exceeds %d-byte limit", ErrInvalidResponse, maxAudioBytes)
		}
		if last != nil && response.Output.MIME != last.Output.MIME {
			return nil, fmt.Errorf("%w: audio MIME type changed from %q to %q", ErrInvalidResponse, last.Output.MIME, response.Output.MIME)
		}
		audio = append(audio, response.Output.Audio...)
		last = &Response{Output: &Output{MIME: response.Output.MIME, Audio: audio, Metadata: response.Output.Metadata.Clone()}}
		if response.Metadata != nil {
			responseMetadata := *response.Metadata
			responseMetadata.Extra = response.Metadata.Extra.Clone()
			last.Metadata = &responseMetadata
		}
	}
	if last == nil {
		return nil, fmt.Errorf("%w: stream returned no audio", ErrInvalidResponse)
	}
	return last, nil
}
