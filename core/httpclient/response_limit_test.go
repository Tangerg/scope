package httpclient

import (
	"errors"
	"io"
	"math"
	"net/http"
	"strings"
	"testing"
)

type limitTestBody struct {
	reader   io.Reader
	closed   int
	closeErr error
}

func (l *limitTestBody) Read(buffer []byte) (int, error) { return l.reader.Read(buffer) }

func (l *limitTestBody) Close() error {
	l.closed++
	return l.closeErr
}

func TestResponseBodyLimit(t *testing.T) {
	for _, sample := range []struct {
		name       string
		status     int
		errorsOnly bool
		body       string
		want       string
		limited    bool
	}{
		{name: "exact JSON", status: http.StatusOK, body: "12345678", want: "12345678"},
		{name: "oversized JSON", status: http.StatusOK, body: "123456789", want: "12345678", limited: true},
		{name: "oversized error", status: http.StatusBadGateway, errorsOnly: true, body: "123456789", want: "12345678", limited: true},
		{name: "unlimited successful stream", status: http.StatusOK, errorsOnly: true, body: "123456789", want: "123456789"},
	} {
		t.Run(sample.name, func(t *testing.T) {
			closeErr := errors.New("close failed")
			body := &limitTestBody{reader: strings.NewReader(sample.body), closeErr: closeErr}
			response := &http.Response{StatusCode: sample.status, Header: http.Header{"Request-Id": {"id"}}, Body: body, ContentLength: 1}
			next := func(*http.Request) (*http.Response, error) { return response, nil }
			apply := ResponseBodyLimit(8).Apply
			if sample.errorsOnly {
				apply = ResponseBodyLimit(8).ApplyErrors
			}
			bounded, err := apply(nil, next)
			if err != nil || bounded != response || body.closed != 0 {
				t.Fatalf("response=%v error=%v closed=%d", bounded, err, body.closed)
			}
			data, err := io.ReadAll(bounded.Body)
			limitErr, limited := errors.AsType[*http.MaxBytesError](err)
			if string(data) != sample.want || limited != sample.limited || limited && limitErr.Limit != 8 || !limited && err != nil {
				t.Fatalf("data=%q error=%v", data, err)
			}
			if err := bounded.Body.Close(); !errors.Is(err, closeErr) || body.closed != 1 {
				t.Fatalf("close error=%v calls=%d", err, body.closed)
			}
			if bounded.ContentLength != 1 || bounded.Header.Get("Request-Id") != "id" {
				t.Fatal("response evidence changed")
			}
		})
	}
}

func TestResponseBodyLimitAdmissionAndTransportErrors(t *testing.T) {
	for _, limit := range []ResponseBodyLimit{0, -1} {
		calls := 0
		_, err := limit.Apply(nil, func(*http.Request) (*http.Response, error) { calls++; return nil, nil })
		if err == nil || calls != 0 {
			t.Fatalf("limit=%d calls=%d error=%v", limit, calls, err)
		}
	}
	if _, err := ResponseBodyLimit(1).Apply(nil, nil); err == nil {
		t.Fatal("nil handler admitted")
	}
	fault := errors.New("transport failed")
	for _, sample := range []struct {
		response *http.Response
		err      error
	}{
		{}, {response: &http.Response{}}, {response: &http.Response{}, err: fault},
	} {
		response, err := ResponseBodyLimit(1).Apply(nil, func(*http.Request) (*http.Response, error) { return sample.response, sample.err })
		if response != sample.response || !errors.Is(err, sample.err) {
			t.Fatalf("response=%v error=%v", response, err)
		}
	}
}

func TestEventStreamLimitPreservesFramingAndTailErrors(t *testing.T) {
	for _, wire := range []string{
		"data: one\n\n", "data:\r\n\r\n", "data:\ndata: two\n\n", ": comment\n\n",
		"\xef\xbb\xbfdata: one\n\n", "\n\r\n", "data: unfinished", "data: [DONE]\n\n",
	} {
		t.Run(wire, func(t *testing.T) {
			for _, readSize := range []int{1, 3, 64} {
				body := &limitTestBody{reader: strings.NewReader(wire + wire)}
				limit := len(wire)
				if wire == "data: unfinished" {
					limit *= 2
				}
				response, err := ResponseBodyLimit(limit).ApplyEventStream(nil, func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
				})
				if err != nil {
					t.Fatal(err)
				}
				var got strings.Builder
				buffer := make([]byte, readSize)
				for {
					count, err := response.Body.Read(buffer)
					got.Write(buffer[:count])
					if err != nil {
						if !errors.Is(err, io.EOF) {
							t.Fatal(err)
						}
						break
					}
				}
				if got.String() != wire+wire {
					t.Fatalf("got=%q", got.String())
				}
				if _, err := response.Body.Read(buffer); !errors.Is(err, io.EOF) {
					t.Fatalf("terminal read=%v", err)
				}
				if err := response.Body.Close(); err != nil || body.closed != 1 {
					t.Fatalf("close=%v calls=%d", err, body.closed)
				}
			}
		})
	}
	fault := errors.New("stream failed")
	body := &limitTestBody{reader: io.MultiReader(strings.NewReader("data: ok\n\n"), limitErrorReader{err: errors.Join(io.EOF, fault)})}
	response, err := ResponseBodyLimit(math.MaxInt64).ApplyEventStream(nil, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: body}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if string(data) != "data: ok\n\n" || !errors.Is(err, fault) {
		t.Fatalf("data=%q error=%v", data, err)
	}
}

type limitErrorReader struct{ err error }

func (l limitErrorReader) Read([]byte) (int, error) { return 0, l.err }

func TestEventStreamLimitRejectsAccumulatedMultilineEvent(t *testing.T) {
	wire := "data: 12\ndata: 34\n\n"
	response, err := ResponseBodyLimit(16).ApplyEventStream(nil, func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(wire))}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	if _, limited := errors.AsType[*http.MaxBytesError](err); !limited || len(data) != 16 {
		t.Fatalf("data=%q error=%v", data, err)
	}
	for _, sample := range []struct {
		status int
		body   io.ReadCloser
		err    error
	}{
		{status: http.StatusBadRequest, body: io.NopCloser(strings.NewReader(wire))},
		{status: http.StatusOK}, {err: errors.New("dial failed")},
	} {
		response, err := ResponseBodyLimit(1).ApplyEventStream(nil, func(*http.Request) (*http.Response, error) {
			if sample.err != nil {
				return nil, sample.err
			}
			return &http.Response{StatusCode: sample.status, Body: sample.body}, nil
		})
		if !errors.Is(err, sample.err) {
			t.Fatal(err)
		}
		if sample.body != nil {
			if _, err := io.ReadAll(response.Body); err == nil {
				t.Fatal("unbounded error envelope")
			}
		}
	}
}
