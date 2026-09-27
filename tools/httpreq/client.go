package httpreq

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/Tangerg/scope/tools/content"
)

// Client executes requests through an immutable network and resource policy.
type Client struct {
	transport        *resty.Client
	allowedHosts     Allowlist
	allowedMethods   map[Method]struct{}
	maxResponseBytes int64
	defaultTimeout   time.Duration
}

// NewClient fails closed: without an allowlist there is no host a model-
// supplied URL may reach. The alternative — an empty allowlist meaning
// unrestricted — turns a forgotten configuration line into an open proxy.
func NewClient(config ClientConfig) (*Client, error) {
	policy, err := config.compilePolicy()
	if err != nil {
		return nil, err
	}

	var transport *resty.Client
	if config.HTTPClient != nil {
		// Resty's redirect policy mutates the underlying http.Client. A shallow
		// clone preserves caller ownership while intentionally sharing Transport
		// and Jar, whose concurrency contracts come from net/http.
		httpClient := *config.HTTPClient
		transport = resty.NewWithClient(&httpClient)
	} else {
		transport = resty.New()
	}
	transport.SetRedirectPolicy(resty.RedirectPolicyFunc(policy.checkRedirect))
	for name, value := range config.DefaultHeaders {
		transport.SetHeader(name, value)
	}

	return &Client{
		transport:        transport,
		allowedHosts:     policy.allowedHosts,
		allowedMethods:   policy.allowedMethods,
		maxResponseBytes: policy.maxResponseBytes,
		defaultTimeout:   policy.defaultTimeout,
	}, nil
}

// Do applies the frozen host, method, timeout, redirect, and response-size
// policy before returning a model-facing response.
// If body reading or closure fails, the response retains the received status,
// headers, and admitted body prefix as evidence alongside the error. It is not
// a completed response, and the error does not establish whether a request with
// side effects committed at the server.
func (c *Client) Do(ctx context.Context, request *Request) (*Response, error) {
	if c == nil {
		return nil, ErrNilClient
	}
	prepared, err := request.prepare()
	if err != nil {
		return nil, err
	}
	method := prepared.Method
	if _, allowed := c.allowedMethods[method]; !allowed {
		return nil, fmt.Errorf("%w: %s", ErrMethodNotAllowed, method)
	}

	parsedURL, err := url.Parse(prepared.URL)
	if err != nil {
		return nil, fmt.Errorf("httpreq: parse validated request URL: %w", err)
	}
	host := parsedURL.Hostname()
	if !c.allowedHosts.Allows(host) {
		return nil, fmt.Errorf("%w: %s", ErrHostNotAllowed, host)
	}

	timeout := c.defaultTimeout
	if prepared.TimeoutMS > 0 {
		timeout = time.Duration(prepared.TimeoutMS) * time.Millisecond
	}
	callContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	restyRequest := c.transport.R().
		SetContext(callContext).
		SetDoNotParseResponse(true)
	for name, value := range prepared.Headers {
		restyRequest.SetHeader(name, value)
	}
	for name, value := range prepared.Query {
		restyRequest.SetQueryParam(name, value)
	}
	if prepared.Body != "" {
		restyRequest.SetBody(prepared.Body)
	}

	startedAt := time.Now()
	response, err := restyRequest.Execute(string(method), prepared.URL)
	if err != nil {
		return nil, fmt.Errorf("httpreq: execute %s request to host %q: %w", method, host, err)
	}
	bodyReader := response.RawBody()
	body, truncated, err := readCapped(bodyReader, c.maxResponseBytes)
	err = errors.Join(err, bodyReader.Close())
	headers := make(map[string][]content.Content, len(response.Header()))
	for name, values := range response.Header() {
		for _, value := range values {
			headers[name] = append(headers[name], content.New([]byte(value)))
		}
	}
	result := &Response{
		Status:    response.StatusCode(),
		Headers:   headers,
		Body:      content.New(body),
		Truncated: truncated,
		Duration:  time.Since(startedAt).String(),
	}
	if err != nil {
		return result, fmt.Errorf("httpreq: consume response body from host %q: %w", host, err)
	}
	return result, nil
}
