package weaviate

import (
	"context"
	"crypto/tls"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	pb "github.com/weaviate/weaviate/grpc/generated/protocol/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"

	"github.com/Tangerg/scope/core/httpclient"
)

// Client binds all REST operations to one origin and gRPC calls to one configured server.
// Multiple stores can borrow the connection. The Host closes it after use.
type Client struct {
	rpc    pb.WeaviateClient
	conn   *grpc.ClientConn
	http   *http.Client
	base   *url.URL
	closed atomic.Bool
	key    string
}

type ClientConfig struct {
	Endpoint    string
	GRPCAddress string
	APIKey      string
}

func NewClient(ctx context.Context, c ClientConfig) (*Client, error) {
	u, err := url.Parse(c.Endpoint)
	if err != nil || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.EscapedPath() != "" && u.EscapedPath() != "/" || u.Scheme != "http" && u.Scheme != "https" {
		return nil, errors.New("weaviate: endpoint must be an HTTP(S) origin")
	}
	grpcHost, grpcPort, err := net.SplitHostPort(c.GRPCAddress)
	if err != nil || grpcHost == "" || grpcPort == "" || strings.ContainsAny(grpcHost, "/?#@") {
		return nil, errors.New("weaviate: gRPC address requires a host and port")
	}
	portNumber, portErr := strconv.ParseUint(grpcPort, 10, 16)
	if portErr != nil || portNumber == 0 {
		return nil, errors.New("weaviate: gRPC port must be in [1,65535]")
	}
	h, err := httpclient.New(ctx, httpclient.Config{HTTPClient: &http.Client{Timeout: 30 * time.Second}, BaseURL: u.String(), RejectRedirects: true})
	if err != nil {
		return nil, err
	}
	client := &Client{http: h, base: u, key: c.APIKey}
	var meta struct {
		MaxBytes int `json:"grpcMaxMessageSize"`
	}
	if err = client.readJSON(ctx, u.JoinPath("v1", "meta"), &meta); err != nil {
		return nil, fmt.Errorf("weaviate: read server metadata: %w", err)
	}
	secure := insecure.NewCredentials()
	if u.Scheme == "https" {
		secure = credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS12})
	}
	opts := []grpc.DialOption{grpc.WithTransportCredentials(secure), grpc.WithDisableRetry(), grpc.WithDisableServiceConfig()}
	if meta.MaxBytes > 0 {
		opts = append(opts, grpc.WithDefaultCallOptions(grpc.MaxCallRecvMsgSize(meta.MaxBytes), grpc.MaxCallSendMsgSize(meta.MaxBytes)))
	}
	conn, err := grpc.NewClient("passthrough:///"+net.JoinHostPort(grpcHost, grpcPort), opts...)
	if err != nil {
		return nil, err
	}
	client.conn = conn
	client.rpc = pb.NewWeaviateClient(conn)
	return client, nil
}

// Close revokes future requests and closes the gRPC connection. HTTP requests
// admitted before Close remain governed by the calling context.
func (c *Client) Close() error {
	if c.closed.Swap(true) {
		return nil
	}
	return c.conn.Close()
}

func (c *Client) callContext(ctx context.Context) context.Context {
	var md metadata.MD
	if c.key != "" {
		md = metadata.Pairs("authorization", "Bearer "+c.key)
	}
	return metadata.NewOutgoingContext(ctx, md)
}

const maxSchemaBytes = 4 << 20

// SDK v6 drops multivector.enabled when decoding HNSW. Read the collection
// contract once from the native schema, which distinguishes false from true.
func (c *Client) schema(ctx context.Context, name string) (collectionWire, error) {
	var raw collectionWire
	err := c.readJSON(ctx, c.base.JoinPath("v1", "schema", name), &raw)
	return raw, err
}

func (c *Client) request(ctx context.Context, method string, u *url.URL) (*http.Response, error) {
	if c.closed.Load() {
		return nil, net.ErrClosed
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.key != "" {
		req.Header.Set("Authorization", "Bearer "+c.key)
	}
	return c.http.Do(req)
}

func (c *Client) readJSON(ctx context.Context, u *url.URL, target any) error {
	resp, err := c.request(ctx, http.MethodGet, u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("weaviate: GET %s: HTTP %d", u.Path, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxSchemaBytes+1))
	if err != nil {
		return err
	}
	if len(body) > maxSchemaBytes {
		return errors.New("weaviate: response exceeds limit")
	}
	return jsonv2.Unmarshal(body, target)
}

func (c *Client) delete(ctx context.Context, collection, id string) error {
	resp, err := c.request(ctx, http.MethodDelete, c.base.JoinPath("v1", "objects", collection, id))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("weaviate: delete object: HTTP %d", resp.StatusCode)
	}
	return nil
}

func validCollectionName(name string) bool {
	// A collection is a single path segment and a native identifier.
	if name == "" || strings.TrimSpace(name) != name {
		return false
	}
	for i, ch := range name {
		if ch >= 'A' && ch <= 'Z' || ch >= 'a' && ch <= 'z' || ch == '_' || i > 0 && ch >= '0' && ch <= '9' {
			continue
		}
		return false
	}
	return true
}
