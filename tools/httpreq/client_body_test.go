package httpreq_test

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"testing/iotest"
	"testing/synctest"
	"time"

	"github.com/Tangerg/scope/tools/httpreq"
)

type responseTransport struct{ body io.ReadCloser }

func (r responseTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: r.body, Request: request}, nil
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
		if response.Body != "payload" || duration != 2*time.Second {
			t.Fatalf("response body = %q, duration = %s; want payload, 2s", response.Body, duration)
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
				body.Reader = iotest.ErrReader(testCase.readErr)
			}
			client := clientWithBody(t, body)
			response, err := client.Do(t.Context(), &httpreq.Request{URL: "https://example.com"})
			for _, cause := range []error{testCase.readErr, testCase.closeErr} {
				if cause != nil && !errors.Is(err, cause) {
					t.Errorf("error = %v, want cause %v", err, cause)
				}
			}
			if response != nil || body.closed != 1 {
				t.Errorf("response = %#v, closes = %d; want nil, 1", response, body.closed)
			}
		})
	}
}
