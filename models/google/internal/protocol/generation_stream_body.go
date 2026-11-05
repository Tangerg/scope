package protocol

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"net/http"
)

// Google can send a bare JSON error after HTTP 200, including after SSE data.
// Detect that native envelope without interpreting or buffering SSE events.
type generationStreamBody struct {
	reader    io.Reader
	client    *GenerationClient
	headers   http.Header
	lineStart bool
	pending   []byte
	readErr   error
	err       error
}

func (g *generationStreamBody) Read(target []byte) (int, error) {
	if g.err != nil {
		return 0, g.err
	}
	if g.pending == nil {
		count, err := g.reader.Read(target)
		for index, value := range target[:count] {
			if g.lineStart && value == '{' {
				g.pending = bytes.Clone(target[index:count])
				g.readErr = err
				count, err = index, nil
				break
			}
			switch value {
			case '\r', '\n':
				g.lineStart = true
			case ' ', '\t':
			default:
				g.lineStart = false
			}
		}
		if g.pending == nil {
			// Fixed-size probes can consume data and discard its terminal error.
			g.err = err
		}
		// A partial error must not delay already complete SSE data in this read.
		if g.pending == nil || count > 0 {
			return count, err
		}
	}
	tail := g.reader
	if g.readErr != nil {
		tail = bytes.NewReader(nil)
	}
	g.err = g.decodeError(io.MultiReader(bytes.NewReader(g.pending), tail))
	if g.readErr != io.EOF { //nolint:errorlint // A joined EOF can carry an I/O failure that must remain in the error chain.
		g.err = errors.Join(g.err, g.readErr)
	}
	g.pending = nil
	return 0, g.err
}

func (g *generationStreamBody) decodeError(reader io.Reader) error {
	raw, err := jsontext.NewDecoder(io.LimitReader(reader, maxGenerationEventBytes+1)).ReadValue()
	if err != nil {
		return err
	}
	if len(raw) > maxGenerationEventBytes {
		return errors.New("google: generation error exceeds byte limit")
	}
	if _, err := g.client.decode(raw, g.headers); err != nil {
		return err
	}
	return errors.New("google: generation stream contains a bare JSON response instead of an SSE event")
}
