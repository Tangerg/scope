package httpreq

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/samber/lo"

	"github.com/Tangerg/scope/core/httpclient"
	"github.com/Tangerg/scope/tools/content"
)

// Client executes requests through an immutable network and resource policy.
type Client struct {
	transport *http.Client
	headers   http.Header
	policy    clientPolicy
}

func NewClient(config ClientConfig) (*Client, error) {
	policy, err := config.compilePolicy()
	if err != nil {
		return nil, err
	}

	transport := new(http.Client)
	if config.HTTPClient != nil {
		*transport = *config.HTTPClient
	}
	if lo.IsNil(transport.Transport) {
		transport.Transport = policy.addresses.transport()
	}
	hostRedirect := transport.CheckRedirect
	transport.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := policy.checkRedirect(req, via); err != nil {
			return err
		}
		if hostRedirect != nil {
			if err := hostRedirect(req, via); err != nil {
				return err
			}
			// A host callback may rewrite the destination or method.
			if err := policy.checkRedirect(req, via); err != nil {
				return err
			}
		}
		if len(via) == 0 {
			return nil
		}
		origin, err := httpclient.NewOrigin(via[0].URL)
		if err != nil {
			return err
		}
		destination, err := httpclient.NewOrigin(req.URL)
		if err != nil {
			return err
		}
		if origin != destination {
			if req.Body != nil && req.Body != http.NoBody {
				return ErrRedirectBody
			}
			// All carried headers can contain application secrets. The new
			// origin's cookie jar and transport still apply on the actual send.
			clear(req.Header)
			req.Host = req.URL.Host
		}
		return nil
	}
	headers := make(http.Header, len(config.DefaultHeaders))
	for name, value := range config.DefaultHeaders {
		headers.Set(name, value)
	}
	return &Client{transport: transport, headers: headers, policy: policy}, nil
}

// Do applies the frozen client policy. If body reading or closure fails, the
// returned Response retains the received status, headers, and admitted body
// prefix as evidence alongside the error. The error does not establish whether
// a request with side effects committed at the server.
func (c *Client) Do(ctx context.Context, request *Request) (*Response, error) {
	if c == nil {
		return nil, ErrNilClient
	}
	prepared, host, err := c.admit(request)
	if err != nil {
		return nil, err
	}
	callContext, cancel := context.WithTimeout(ctx, prepared.timeout(c.policy.defaultTimeout))
	defer cancel()

	var payload io.Reader
	if prepared.Body != "" {
		payload = strings.NewReader(prepared.Body)
	}
	wire, err := http.NewRequestWithContext(callContext, string(prepared.Method), prepared.URL, payload)
	if err != nil {
		return nil, &admissionError{err}
	}
	if len(prepared.Query) != 0 {
		values := wire.URL.Query()
		for key, value := range prepared.Query {
			values.Set(key, value)
		}
		wire.URL.RawQuery = values.Encode()
	}
	wire.Header = c.headers.Clone()
	for name, value := range prepared.Headers {
		wire.Header.Set(name, value)
	}
	if authority := wire.Header.Get(authorityHeader); authority != "" {
		wire.Host = authority
		wire.Header.Del(authorityHeader)
	}
	startedAt := time.Now()
	response, err := c.transport.Do(wire)
	if err != nil {
		return nil, fmt.Errorf("httpreq: execute %s request to host %q: %w", prepared.Method, host, err)
	}
	bodyReader := response.Body
	body, truncated, err := readCapped(bodyReader, c.policy.maxResponseBytes)
	err = errors.Join(err, bodyReader.Close())
	result := &Response{
		Status:    response.StatusCode,
		Headers:   newResponseHeaders(response.Header),
		Body:      content.New(body),
		Truncated: truncated,
		Duration:  time.Since(startedAt).String(),
	}
	if err != nil {
		return result, fmt.Errorf("httpreq: consume response body from host %q: %w", host, err)
	}
	return result, nil
}

// admissionError marks a request Do refused before sending anything. Redirect
// checks share the policy sentinels after the first request was already sent,
// so only this type, not the sentinel, proves the request never left.
type admissionError struct{ err error }

func (a *admissionError) Error() string { return a.err.Error() }
func (a *admissionError) Unwrap() error { return a.err }

func (c *Client) admit(request *Request) (*Request, string, error) {
	prepared, err := request.prepare()
	if err != nil {
		return nil, "", &admissionError{err}
	}
	if !c.policy.allowsMethod(prepared.Method) {
		return nil, "", &admissionError{fmt.Errorf("%w: %s", ErrMethodNotAllowed, prepared.Method)}
	}
	parsedURL, err := url.Parse(prepared.URL)
	if err != nil {
		return nil, "", &admissionError{fmt.Errorf("httpreq: parse validated request URL: %w", err)}
	}
	if !c.policy.allowedHosts.Allows(parsedURL) {
		return nil, "", &admissionError{fmt.Errorf("%w: %s", ErrHostNotAllowed, parsedURL.Host)}
	}
	return prepared, parsedURL.Hostname(), nil
}
