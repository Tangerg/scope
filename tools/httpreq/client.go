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
	transport *resty.Client
	policy    clientPolicy
}

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
	transport.SetHeaders(config.DefaultHeaders)
	return &Client{transport: transport, policy: policy}, nil
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

	restyRequest := c.transport.R().
		SetContext(callContext).
		SetDoNotParseResponse(true).
		SetHeaders(prepared.Headers).
		SetQueryParams(prepared.Query)
	if prepared.Body != "" {
		restyRequest.SetBody(prepared.Body)
	}

	startedAt := time.Now()
	response, err := restyRequest.Execute(string(prepared.Method), prepared.URL)
	if err != nil {
		return nil, fmt.Errorf("httpreq: execute %s request to host %q: %w", prepared.Method, host, err)
	}
	bodyReader := response.RawBody()
	body, truncated, err := readCapped(bodyReader, c.policy.maxResponseBytes)
	err = errors.Join(err, bodyReader.Close())
	result := &Response{
		Status:    response.StatusCode(),
		Headers:   newResponseHeaders(response.Header()),
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
	host := parsedURL.Hostname()
	if !c.policy.allowedHosts.Allows(host) {
		return nil, "", &admissionError{fmt.Errorf("%w: %s", ErrHostNotAllowed, host)}
	}
	return prepared, host, nil
}
