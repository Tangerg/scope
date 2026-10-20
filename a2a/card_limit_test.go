package a2a

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
)

func TestAgentCardResponseByteLimit(t *testing.T) {
	for _, mode := range []string{"plain", "chunked", "gzip", "redirect"} {
		for _, excess := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/excess=%t", mode, excess), func(t *testing.T) {
				var payload []byte
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if mode == "redirect" && r.URL.Path != "/card" {
						http.Redirect(w, r, "/card", http.StatusFound)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					if mode == "chunked" {
						w.(http.Flusher).Flush()
					}
					if mode == "gzip" {
						w.Header().Set("Content-Encoding", "gzip")
						compressed := gzip.NewWriter(w)
						if _, err := compressed.Write(payload); err != nil {
							t.Error(err)
						}
						if err := compressed.Close(); err != nil {
							t.Error(err)
						}
						return
					}
					if _, err := w.Write(payload); err != nil {
						t.Error(err)
					}
				}))
				t.Cleanup(server.Close)
				var err error
				payload, err = jsonv2.Marshal(testAgentCard(server.URL + "/rpc"))
				if err != nil {
					t.Fatal(err)
				}
				limit := int64(len(payload))
				if excess {
					payload = append(payload, ' ')
				}
				set, err := OpenToolSet(t.Context(), Endpoint{CardURL: server.URL, HTTPClient: server.Client(), CardMaxBytes: limit})
				if set != nil {
					t.Cleanup(func() {
						if closeErr := set.Close(); closeErr != nil {
							t.Error(closeErr)
						}
					})
				}
				if excess {
					sizeErr, ok := errors.AsType[*http.MaxBytesError](err)
					if !ok || sizeErr.Limit != limit || set != nil {
						t.Fatalf("OpenToolSet() returned set=%t, error=%v; want size error at %d", set != nil, err, limit)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
			})
		}
	}
}

type cardBody struct {
	reader io.Reader
	closed bool
}

func (c *cardBody) Read(data []byte) (int, error) { return c.reader.Read(data) }
func (c *cardBody) Close() error                  { c.closed = true; return nil }

type cardResponseTransport struct {
	body   *cardBody
	length int64
	status int
}

func (c *cardResponseTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: c.status, Status: http.StatusText(c.status), Header: make(http.Header), ContentLength: c.length, Body: c.body}, nil
}

func TestAgentCardBodyClosureAndClientOwnership(t *testing.T) {
	readFailure := errors.New("card read failed")
	raw, err := jsonv2.Marshal(testAgentCard("https://agent.example/rpc"))
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name          string
		reader        io.Reader
		length, limit int64
		status        int
		want          error
		sizeError     bool
	}{
		{name: "success", reader: bytes.NewReader(raw), length: int64(len(raw)), limit: int64(len(raw)), status: 200},
		{name: "unknown length", reader: bytes.NewReader(raw), length: -1, limit: int64(len(raw)), status: 200},
		{name: "misleading length", reader: bytes.NewReader(append(bytes.Clone(raw), ' ')), length: 1, limit: int64(len(raw)), status: 200, sizeError: true},
		{name: "maximum bound", reader: bytes.NewReader(raw), length: -1, limit: math.MaxInt64, status: 200},
		{name: "default bound", reader: strings.NewReader(string(raw) + strings.Repeat(" ", int(defaultCardMaxBytes))), length: -1, status: 200, sizeError: true},
		{name: "read failure", reader: cardReadError{readFailure}, length: -1, limit: 1024, status: 200, want: readFailure},
		{name: "bad status", reader: bytes.NewReader(raw), length: -1, limit: 1, status: 503},
	} {
		t.Run(test.name, func(t *testing.T) {
			body := &cardBody{reader: test.reader}
			transport := &cardResponseTransport{body: body, length: test.length, status: test.status}
			client := &http.Client{Transport: transport}
			set, err := OpenToolSet(t.Context(), Endpoint{CardURL: "https://agent.example", HTTPClient: client, CardMaxBytes: test.limit})
			if set != nil {
				if closeErr := set.Close(); closeErr != nil {
					t.Fatal(closeErr)
				}
			}
			if !body.closed {
				t.Fatal("response body not closed")
			}
			if client.Transport != transport || client.CheckRedirect != nil {
				t.Fatal("caller client mutated")
			}
			if test.sizeError {
				if _, ok := errors.AsType[*http.MaxBytesError](err); !ok {
					t.Fatalf("error = %v, want MaxBytesError", err)
				}
			} else if test.want != nil {
				if !errors.Is(err, test.want) {
					t.Fatalf("error = %v, want %v", err, test.want)
				}
			} else if test.status != 200 {
				status, ok := errors.AsType[*agentcard.ErrStatusNotOK](err)
				if !ok || status.StatusCode != test.status {
					t.Fatalf("error = %v, want status %d", err, test.status)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}

type cardReadError struct{ cause error }

func (c cardReadError) Read([]byte) (int, error) { return 0, c.cause }

type canceledCardBody struct {
	ctx    context.Context
	closed bool
}

func (c *canceledCardBody) Read([]byte) (int, error) { <-c.ctx.Done(); return 0, c.ctx.Err() }
func (c *canceledCardBody) Close() error             { c.closed = true; return nil }

type canceledCardTransport struct{ body *canceledCardBody }

func (c *canceledCardTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	c.body = &canceledCardBody{ctx: request.Context()}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: c.body}, nil
}

func TestAgentCardCanceledReadClosesBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		transport := &canceledCardTransport{}
		_, err := OpenToolSet(t.Context(), Endpoint{CardURL: "https://agent.example", HTTPClient: &http.Client{Transport: transport}, CardTimeout: time.Second})
		if !errors.Is(err, context.DeadlineExceeded) || transport.body == nil || !transport.body.closed {
			t.Fatalf("resolution = %v, body = %+v", err, transport.body)
		}
	})
}

func TestAgentCardLimitDoesNotBoundRPCResponses(t *testing.T) {
	const limit = 512
	answer := strings.Repeat("evidence", limit)
	var endpoint string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "application/json")
		if request.Method == http.MethodGet {
			card, err := jsonv2.Marshal(testAgentCard(endpoint))
			if err != nil || len(card) > limit {
				t.Errorf("fixture card = %s, %v", card, err)
				return
			}
			_, _ = writer.Write(card)
			return
		}
		var rpc struct {
			ID json.RawMessage `json:"id"`
		}
		if err := jsonv2.UnmarshalRead(request.Body, &rpc); err != nil {
			t.Error(err)
			return
		}
		_, _ = fmt.Fprintf(writer, `{"jsonrpc":"2.0","id":%s,"result":{"message":{"messageId":"reply","role":"ROLE_AGENT","parts":[{"text":%q}]}}}`, rpc.ID, answer)
	}))
	t.Cleanup(server.Close)
	endpoint = server.URL + "/rpc"
	set, err := OpenToolSet(t.Context(), Endpoint{CardURL: server.URL, HTTPClient: server.Client(), CardMaxBytes: limit})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := set.Close(); closeErr != nil {
			t.Error(closeErr)
		}
	})
	binding, err := tool.Bind(set.Tools()[0])
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{ID: "call", Name: binding.Contract().Definition().Name, Arguments: `{"message":"question"}`}, nil)
	if err != nil {
		t.Fatal(err)
	}
	output, err := binding.Call(t.Context(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	text, present := output.Output.Text()
	if !present || text != answer {
		t.Fatalf("RPC output = %q, present=%t", text, present)
	}
}
