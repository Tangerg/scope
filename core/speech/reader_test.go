package speech_test

import (
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/Tangerg/scope/core/speech"
)

type readStep struct {
	data string
	err  error
}

type chunkReader struct {
	steps  []readStep
	reads  int
	closed bool
}

func (c *chunkReader) Read(buffer []byte) (int, error) {
	c.reads++
	if len(c.steps) == 0 {
		return 0, io.EOF
	}
	step := c.steps[0]
	c.steps = c.steps[1:]
	return copy(buffer, step.data), step.err
}

func (c *chunkReader) Close() error {
	c.closed = true
	return nil
}

func TestReadChunksPreservesBytesBeforeTerminalError(t *testing.T) {
	for _, test := range []struct {
		name   string
		step   readStep
		events []string
	}{
		{name: "bytes and EOF", step: readStep{data: "audio", err: io.EOF}, events: []string{"audio"}},
		{name: "bytes and failure", step: readStep{data: "audio", err: io.ErrUnexpectedEOF}, events: []string{"audio", "error"}},
		{name: "empty EOF", step: readStep{err: io.EOF}},
		{name: "empty failure", step: readStep{err: io.ErrUnexpectedEOF}, events: []string{"error"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			reader := &chunkReader{steps: []readStep{test.step}}
			chunks := speech.ReadChunks(reader)
			if reader.reads != 0 {
				t.Fatal("ReadChunks performed eager I/O")
			}
			var events []string
			for data, err := range chunks {
				if err != nil {
					if data != nil || !errors.Is(err, test.step.err) {
						t.Fatalf("terminal event = %q, %v; want nil, %v", data, err, test.step.err)
					}
					events = append(events, "error")
					continue
				}
				events = append(events, string(data))
			}
			if !slices.Equal(events, test.events) || reader.reads != 1 || reader.closed {
				t.Fatalf("events = %v; reads = %d; closed = %v", events, reader.reads, reader.closed)
			}
		})
	}
}

func TestReadChunksTransfersIndependentBuffers(t *testing.T) {
	reader := &chunkReader{steps: []readStep{{data: "first"}, {data: "second"}, {data: "third", err: io.EOF}}}
	var chunks [][]byte
	for data, err := range speech.ReadChunks(reader) {
		if err != nil {
			t.Fatal(err)
		}
		chunks = append(chunks, data)
	}
	if len(chunks) != 3 || string(chunks[0]) != "first" || string(chunks[1]) != "second" || string(chunks[2]) != "third" {
		t.Fatalf("retained chunks = %q", chunks)
	}
	chunks[0][0] = 'F'
	if string(chunks[1]) != "second" || string(chunks[2]) != "third" {
		t.Fatalf("caller mutation changed another chunk: %q", chunks)
	}
}

func TestReadChunksStopsWithoutFurtherReadsOrErrors(t *testing.T) {
	for _, readErr := range []error{nil, io.ErrUnexpectedEOF} {
		reader := &chunkReader{steps: []readStep{{data: "first", err: readErr}, {data: "second", err: io.EOF}}}
		for data, err := range speech.ReadChunks(reader) {
			if err != nil || string(data) != "first" {
				t.Fatalf("first event = %q, %v", data, err)
			}
			break
		}
		if reader.reads != 1 || reader.closed {
			t.Fatalf("reads = %d; closed = %v", reader.reads, reader.closed)
		}
	}
}

func TestReadChunksRejectsNilReaders(t *testing.T) {
	for _, reader := range []io.Reader{nil, (*chunkReader)(nil)} {
		events := 0
		for data, err := range speech.ReadChunks(reader) {
			events++
			if data != nil || err == nil {
				t.Fatalf("nil reader event = %q, %v", data, err)
			}
		}
		if events != 1 {
			t.Fatalf("nil reader yielded %d events", events)
		}
	}
}
