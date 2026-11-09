package elastic

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"io"
	"math"
	"net/http"
	"net/url"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/core/vectorstore"
)

const (
	DefaultMaxResponseBytes int64 = 16 << 20
	mediaTypeJSON                 = "application/json"
	mediaTypeNDJSON               = "application/x-ndjson"
	bulkPath                      = "/_bulk"
)

var ErrResponseLimit = errors.New("elastic: native response exceeds MaxResponseBytes")

type ClientConfig struct {
	Perform          func(*http.Request) (*http.Response, error)
	MaxResponseBytes int64
}

func (c ClientConfig) Validate() error {
	if c.Perform == nil {
		return errors.New("elastic: native transport is required")
	}
	if c.MaxResponseBytes < 0 || c.MaxResponseBytes == math.MaxInt64 {
		return errors.New("elastic: MaxResponseBytes must allow a bounded response")
	}
	return nil
}

// Client owns bounded response consumption and closure. Perform borrows the
// host's native streaming transport without changing its retry or auth policy.
type Client struct {
	perform          func(*http.Request) (*http.Response, error)
	maxResponseBytes int64
}

func NewClient(config ClientConfig) (*Client, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &Client{perform: config.Perform, maxResponseBytes: cmp.Or(config.MaxResponseBytes, DefaultMaxResponseBytes)}, nil
}

// Read retains received bytes with their errors, because acknowledgments and
// scroll cleanup cursors can remain meaningful after a read or Close failure.
// A zero limit uses the client bound; a positive limit may only narrow it.
func (c *Client) Read(ctx context.Context, method, path string, query url.Values, body []byte, limit int64) (raw []byte, err error) {
	if err = c.validate(); err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	if limit < 0 {
		return nil, errors.New("elastic: response limit must not be negative")
	}
	limit = min(cmp.Or(limit, c.maxResponseBytes), c.maxResponseBytes)
	endpoint := (&url.URL{Path: path, RawQuery: query.Encode()}).String()
	request, err := http.NewRequestWithContext(ctx, method, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", mediaTypeJSON)
	contentType := mediaTypeJSON
	if path == bulkPath {
		contentType = mediaTypeNDJSON
	}
	request.Header.Set("Content-Type", contentType)
	response, callErr := c.perform(request)
	if response == nil || lo.IsNil(response.Body) {
		return nil, errors.Join(callErr, errors.New("elastic: native client returned no response body"))
	}
	defer func() { err = errors.Join(err, response.Body.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(response.Body, limit+1))
	err = errors.Join(callErr, err)
	if response.StatusCode == 0 {
		if callErr != nil {
			return nil, err
		}
		return nil, errors.Join(err, errors.New("elastic: native response has no HTTP status"))
	}
	if int64(len(raw)) > limit {
		if limit < c.maxResponseBytes {
			err = errors.Join(err, vectorstore.ErrScanBudget)
		} else {
			err = errors.Join(err, ErrResponseLimit)
		}
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		err = errors.Join(err, httpclient.NewResponseError(response.StatusCode, response.Header))
	}
	return raw, err
}

func (c *Client) validate() error {
	if c == nil || c.perform == nil {
		return errors.New("elastic: uninitialized protocol client")
	}
	return nil
}
