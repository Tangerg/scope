package protocol

import (
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"

	"github.com/Tangerg/sse"
	"google.golang.org/genai"
)

func TestGenerationStreamBodyPreservesFramingAcrossReads(t *testing.T) {
	const prefix = "\xef\xbb\xbf: heartbeat { ignored }\r\nid: native\revent: message\ndata: {\"candidates\":[\r\ndata: {\"index\":0,\"finishReason\":\"STOP\"}]}\r\n\r\n \t"
	const failure = `{"error":{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`
	for _, chunking := range []struct {
		name string
		wrap func(io.Reader) io.Reader
	}{
		{"whole", func(reader io.Reader) io.Reader { return reader }},
		{"single byte", iotest.OneByteReader},
		{"data and EOF together", iotest.DataErrReader},
	} {
		t.Run(chunking.name, func(t *testing.T) {
			for _, suffix := range []string{"", failure} {
				body := &generationStreamBody{
					reader: chunking.wrap(strings.NewReader(prefix + suffix)), client: &GenerationClient{}, lineStart: true,
				}
				data, err := io.ReadAll(body)
				if string(data) != prefix {
					t.Fatalf("SSE bytes changed: got %q, want %q", data, prefix)
				}
				if suffix == "" {
					if err != nil {
						t.Fatal(err)
					}
					continue
				}
				cause, found := errors.AsType[genai.APIError](err)
				if !found || cause.Code != 429 || cause.Status != "RESOURCE_EXHAUSTED" {
					t.Fatalf("error = %v; want native quota error", err)
				}
			}
		})
	}
}

func TestGenerationStreamBodyRejectsBareNonErrorAndTruncation(t *testing.T) {
	for _, raw := range []string{`{}`, `{"candidates":[{"index":0,"finishReason":"STOP"}]}`, `{"error":`, `{"error":1}`} {
		body := &generationStreamBody{
			reader: iotest.OneByteReader(strings.NewReader(raw)), client: &GenerationClient{}, lineStart: true,
		}
		if data, err := io.ReadAll(body); len(data) != 0 || err == nil {
			t.Fatalf("bare JSON = %s: data=%q error=%v; want failure without SSE bytes", raw, data, err)
		}
	}
}

type generationTailReader struct {
	first     *strings.Reader
	tail      *strings.Reader
	allowTail bool
	tailRead  bool
	readErr   error
}

func (g *generationTailReader) Read(target []byte) (int, error) {
	if g.first.Len() > 0 {
		count, err := g.first.Read(target)
		if g.first.Len() == 0 && g.readErr != nil {
			return count, g.readErr
		}
		return count, err
	}
	g.tailRead = true
	if !g.allowTail {
		return 0, errors.New("error tail was read before publishing complete SSE data")
	}
	return g.tail.Read(target)
}

func TestGenerationStreamBodyReturnsSSEBeforeReadingNativeErrorTail(t *testing.T) {
	const event = "data: {\"candidates\":[{\"index\":0,\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"partial\"}]}}]}\n\n"
	reader := &generationTailReader{
		first: strings.NewReader(event + `{"error":`),
		tail:  strings.NewReader(`{"code":429,"message":"quota exceeded","status":"RESOURCE_EXHAUSTED"}}`),
	}
	body := &generationStreamBody{reader: reader, client: &GenerationClient{}, lineStart: true}
	buffer := make([]byte, len(event)+len(`{"error":`))
	count, err := body.Read(buffer)
	if string(buffer[:count]) != event || err != nil || reader.tailRead {
		t.Fatalf("read = %q, %v; tail read=%t; want complete SSE data without reading tail", buffer[:count], err, reader.tailRead)
	}
	reader.allowTail = true
	data, err := io.ReadAll(body)
	cause, found := errors.AsType[genai.APIError](err)
	if len(data) != 0 || !found || cause.Code != 429 {
		t.Fatalf("remaining body = %q, %v; want native quota error", data, err)
	}
}

func TestGenerationStreamBodyPreservesErrorReportedWithData(t *testing.T) {
	const event = "data: {}\n\n"
	cause := errors.New("response connection lost")
	for _, tail := range []string{`{"error":{"code":429,"message":"quota exceeded"}}`, `{"error":`} {
		reader := &generationTailReader{first: strings.NewReader(event + tail), readErr: cause}
		body := &generationStreamBody{reader: reader, client: &GenerationClient{}, lineStart: true}
		data, err := io.ReadAll(body)
		if string(data) != event || !errors.Is(err, cause) || reader.tailRead {
			t.Fatalf("read = %q, %v; tail read=%t; want original read failure and no retry", data, err, reader.tailRead)
		}
	}
}

func TestGenerationStreamBodyPreservesErrorConsumedBySSEBOMProbe(t *testing.T) {
	cause := errors.New("response connection lost")
	reader := &generationTailReader{
		first: strings.NewReader("dat"), tail: strings.NewReader(""), allowTail: true, readErr: cause,
	}
	body := &generationStreamBody{reader: reader, client: &GenerationClient{}, lineStart: true}
	var failure error
	for _, err := range sse.NewReader(body).Messages() {
		failure = err
	}
	if !errors.Is(failure, cause) || reader.tailRead {
		t.Fatalf("stream error = %v, source reread=%t; want original terminal I/O failure", failure, reader.tailRead)
	}
}
