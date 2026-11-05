package a2a_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	sdka2a "github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"

	"github.com/Tangerg/scope/a2a"
)

const getTaskRequest = `{"jsonrpc":"2.0","id":"1","method":"GetTask","params":{"id":"task-1"}}`

type countingTaskReader struct {
	a2asrv.RequestHandler
	calls *atomic.Int32
}

func (c countingTaskReader) GetTask(context.Context, *sdka2a.GetTaskRequest) (*sdka2a.Task, error) {
	c.calls.Add(1)
	return nil, sdka2a.ErrTaskNotFound
}

// whitespace is an endless request body that is valid JSON padding to any length.
type whitespace struct{}

func (whitespace) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = ' '
	}
	return len(p), nil
}

type countingReader struct {
	io.Reader
	read int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.Reader.Read(p)
	c.read += int64(n)
	return n, err
}

type limitExchange struct {
	calls int32
	read  int64
	wire  string
}

// postGetTask sends a GetTask call padded with leading whitespace to size bytes.
func postGetTask(t *testing.T, maxRequestBytes, size int64) limitExchange {
	t.Helper()
	var calls atomic.Int32
	card := &sdka2a.AgentCard{Name: "bounded", SupportedInterfaces: []*sdka2a.AgentInterface{sdka2a.NewAgentInterface("https://agent.example/invoke", sdka2a.TransportProtocolJSONRPC)}}
	handler, err := a2a.NewProtocolHTTPHandler(a2a.ProtocolServerConfig{Card: card, Handler: countingTaskReader{calls: &calls}, MaxRequestBytes: maxRequestBytes})
	if err != nil {
		t.Fatal(err)
	}
	padding := size - int64(len(getTaskRequest))
	body := &countingReader{Reader: io.MultiReader(io.LimitReader(whitespace{}, padding), strings.NewReader(getTaskRequest))}
	request := httptest.NewRequest(http.MethodPost, "/invoke", body)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set(sdka2a.SvcParamVersion, string(sdka2a.Version))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return limitExchange{calls: calls.Load(), read: body.read, wire: response.Body.String()}
}

func TestProtocolHandlerBoundsRequestBodies(t *testing.T) {
	const limit int64 = 4 << 10
	const defaultLimit int64 = 4 << 20
	for _, test := range []struct {
		name            string
		maxRequestBytes int64
		size            int64
		admitted        bool
	}{
		{name: "at the configured bound", maxRequestBytes: limit, size: limit, admitted: true},
		{name: "past the configured bound", maxRequestBytes: limit, size: limit + 1},
		{name: "an endless body", maxRequestBytes: limit, size: 1 << 30},
		{name: "at the default bound", size: defaultLimit, admitted: true},
		{name: "past the default bound", size: defaultLimit + 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			exchange := postGetTask(t, test.maxRequestBytes, test.size)
			bound := test.maxRequestBytes
			if bound == 0 {
				bound = defaultLimit
			}
			if exchange.read > bound+1 {
				t.Fatalf("server read %d bytes under a %d-byte bound", exchange.read, bound)
			}
			if test.admitted {
				if exchange.calls != 1 || !strings.Contains(exchange.wire, `"code":-32001`) {
					t.Fatalf("request within the bound was not served: calls=%d response=%s", exchange.calls, exchange.wire)
				}
				return
			}
			if exchange.calls != 0 || !strings.Contains(exchange.wire, `"code":-32700`) {
				t.Fatalf("oversized request reached the handler: calls=%d response=%s", exchange.calls, exchange.wire)
			}
		})
	}
}

func TestProtocolHandlerRejectsInvalidConfig(t *testing.T) {
	card := &sdka2a.AgentCard{Name: "test"}
	var calls atomic.Int32
	for _, test := range []struct {
		name   string
		config a2a.ProtocolServerConfig
		want   error
	}{
		{name: "handler", config: a2a.ProtocolServerConfig{Card: card}, want: a2a.ErrNilProtocolHandler},
		{name: "typed nil handler", config: a2a.ProtocolServerConfig{Card: card, Handler: (*a2asrv.InterceptedHandler)(nil)}, want: a2a.ErrNilProtocolHandler},
		{name: "card", config: a2a.ProtocolServerConfig{Handler: countingTaskReader{calls: &calls}}, want: a2a.ErrNilCard},
		{name: "negative request bound", config: a2a.ProtocolServerConfig{Card: card, Handler: countingTaskReader{calls: &calls}, MaxRequestBytes: -1}, want: a2a.ErrInvalidMaxRequestBytes},
	} {
		t.Run(test.name, func(t *testing.T) {
			if handler, err := a2a.NewProtocolHTTPHandler(test.config); !errors.Is(err, test.want) || handler != nil {
				t.Fatalf("NewProtocolHTTPHandler = %v, %v; want %v", handler, err, test.want)
			}
		})
	}
}
