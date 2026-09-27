package httpreq_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/core/chat"
	"github.com/Tangerg/scope/core/tool"
	"github.com/Tangerg/scope/tools/httpreq"
)

type responseTransport struct {
	body    io.ReadCloser
	headers http.Header
	status  int
}

func (r responseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Header: r.headers.Clone(), Body: r.body, Request: request}, nil
}

type responseBody struct {
	io.Reader
	closeErr error
	closed   int
}

func (r *responseBody) Close() error {
	r.closed++
	return r.closeErr
}

func clientWithBody(t *testing.T, body io.ReadCloser) *httpreq.Client {
	t.Helper()
	client, err := httpreq.NewClient(httpreq.ClientConfig{
		AllowedHosts: []string{"example.com"},
		HTTPClient:   &http.Client{Transport: responseTransport{body: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestClientDurationIncludesResponseBody(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		reader, writer := io.Pipe()
		go func() {
			time.Sleep(2 * time.Second)
			if _, err := io.WriteString(writer, "payload"); err != nil {
				t.Error(err)
			}
			if err := writer.Close(); err != nil {
				t.Error(err)
			}
		}()
		client := clientWithBody(t, reader)
		response, err := client.Do(t.Context(), &httpreq.Request{URL: "https://example.com"})
		if err != nil {
			t.Fatal(err)
		}
		duration, err := time.ParseDuration(response.Duration)
		if err != nil {
			t.Fatal(err)
		}
		if string(response.Body.Bytes()) != "payload" || duration != 2*time.Second {
			t.Fatalf("response body = %q, duration = %s; want payload, 2s", response.Body.Bytes(), duration)
		}
	})
}

func TestClientPreservesResponseBodyFailures(t *testing.T) {
	readFailure := errors.New("read failed")
	closeFailure := errors.New("close failed")
	for name, testCase := range map[string]struct{ readErr, closeErr error }{
		"close":          {closeErr: closeFailure},
		"read":           {readErr: readFailure},
		"read and close": {readErr: readFailure, closeErr: closeFailure},
	} {
		t.Run(name, func(t *testing.T) {
			body := &responseBody{Reader: strings.NewReader("payload"), closeErr: testCase.closeErr}
			if testCase.readErr != nil {
				body.Reader = io.MultiReader(strings.NewReader("prefix"), iotest.ErrReader(testCase.readErr))
			}
			client := clientWithBody(t, body)
			response, err := client.Do(t.Context(), &httpreq.Request{URL: "https://example.com"})
			for _, cause := range []error{testCase.readErr, testCase.closeErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Errorf("error = %v, want cause %v", err, cause)
				}
			}
			wantBody := "payload"
			if testCase.readErr != nil {
				wantBody = "prefix"
			}
			if response == nil || response.Status != http.StatusOK || string(response.Body.Bytes()) != wantBody || response.Truncated || body.closed != 1 {
				t.Errorf("response = %#v, closes = %d; want observed status/body and exactly one close", response, body.closed)
			}
		})
	}
}

func TestToolPreservesInterruptedPostResponseAsEvidence(t *testing.T) {
	cause := errors.New("response interrupted after server acknowledgement")
	body := &responseBody{Reader: io.MultiReader(strings.NewReader("created"), iotest.ErrReader(cause))}
	client, err := httpreq.NewClient(httpreq.ClientConfig{
		AllowedHosts: []string{"example.com"}, AllowedMethods: []httpreq.Method{httpreq.MethodPOST},
		HTTPClient: &http.Client{Transport: responseTransport{body: body}},
	})
	if err != nil {
		t.Fatal(err)
	}
	executable, err := httpreq.NewTool(client)
	if err != nil {
		t.Fatal(err)
	}
	binding, err := tool.Bind(executable)
	if err != nil {
		t.Fatal(err)
	}
	invocation, err := binding.Contract().Prepare(chat.ToolCall{
		ID: "post-1", Name: "http_request", Arguments: `{"url":"https://example.com","method":"POST","body":"create"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	output, err := binding.Call(t.Context(), invocation)
	if !errors.Is(err, cause) || !reflect.DeepEqual(output, chat.ToolOutput{}) {
		t.Fatalf("interrupted HTTP invocation = %+v, %v", output, err)
	}
	if failure, found := errors.AsType[*tool.Failure](err); found {
		t.Fatalf("interrupted HTTP response became a definite failure: %v", failure)
	}
	callErr, found := errors.AsType[*tool.CallError](err)
	if !found {
		t.Fatalf("HTTP observations were discarded: %v", err)
	}
	var response httpreq.Response
	if err := jsonv2.Unmarshal(callErr.Evidence().Details, &response); err != nil {
		t.Fatal(err)
	}
	if response.Status != http.StatusOK || string(response.Body.Bytes()) != "created" || response.Truncated || body.closed != 1 {
		t.Fatalf("HTTP evidence = %+v, closes = %d", response, body.closed)
	}
}

func TestToolPreservesNonUTF8BodyAndHeaderObservations(t *testing.T) {
	for _, interrupted := range []bool{false, true} {
		cause := errors.New("response body interrupted")
		prefix := []byte{0xe4, 0xbd}
		var reader io.Reader = bytes.NewReader(append(bytes.Clone(prefix), 0xa0))
		if interrupted {
			reader = io.MultiReader(bytes.NewReader(prefix), iotest.ErrReader(cause))
		}
		body := &responseBody{Reader: reader}
		client, err := httpreq.NewClient(httpreq.ClientConfig{
			AllowedHosts: []string{"example.com"}, MaxResponseBytes: 2,
			HTTPClient: &http.Client{Transport: responseTransport{
				body: body, status: http.StatusCreated,
				headers: http.Header{"X-Commit": {"done"}, "X-Bytes": {string([]byte{0xff, 0x80})}},
			}},
		})
		if err != nil {
			t.Fatal(err)
		}
		executable, err := httpreq.NewTool(client)
		if err != nil {
			t.Fatal(err)
		}
		binding, err := tool.Bind(executable)
		if err != nil {
			t.Fatal(err)
		}
		invocation, err := binding.Contract().Prepare(chat.ToolCall{
			ID: "read-1", Name: "http_request", Arguments: `{"url":"https://example.com"}`,
		})
		if err != nil {
			t.Fatal(err)
		}
		output, err := binding.Call(t.Context(), invocation)
		if interrupted {
			callErr, found := errors.AsType[*tool.CallError](err)
			if !found || !errors.Is(err, cause) {
				t.Fatalf("HTTP byte observations were lost: %v", err)
			}
			output = callErr.Evidence()
		} else if err != nil {
			t.Fatalf("cap splitting UTF8 converted a response to an error: %v", err)
		}
		var response httpreq.Response
		if err := jsonv2.Unmarshal(output.Details, &response); err != nil {
			t.Fatal(err)
		}
		if response.Status != http.StatusCreated || !bytes.Equal(response.Body.Bytes(), prefix) || response.Truncated == interrupted || body.closed != 1 {
			t.Fatalf("HTTP response bytes or observations changed: %+v, closes=%d", response, body.closed)
		}
		if values := response.Headers["X-Bytes"]; len(values) != 1 || !bytes.Equal(values[0].Bytes(), []byte{0xff, 0x80}) {
			t.Fatalf("HTTP obs-text header bytes changed: %+v", response.Headers)
		}
		if values := response.Headers["X-Commit"]; len(values) != 1 || string(values[0].Bytes()) != "done" {
			t.Fatalf("HTTP acknowledgement header changed: %+v", response.Headers)
		}
	}
}
