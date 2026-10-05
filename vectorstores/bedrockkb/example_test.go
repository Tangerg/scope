package bedrockkb_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/bedrockkb"
)

type exampleSigV4Transport struct {
	credentials aws.CredentialsProvider
	region      string
	signer      *v4.Signer
	base        http.RoundTripper
}

func (e exampleSigV4Transport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.GetBody == nil {
		return nil, errors.New("signing requires a replayable request body")
	}
	body, err := request.GetBody()
	if err != nil {
		return nil, err
	}
	defer body.Close()
	digest := sha256.New()
	if _, err = io.Copy(digest, body); err != nil {
		return nil, err
	}
	credentials, err := e.credentials.Retrieve(request.Context())
	if err != nil {
		return nil, err
	}
	signed := request.Clone(request.Context())
	if err = e.signer.SignHTTP(request.Context(), credentials, signed, hex.EncodeToString(digest.Sum(nil)), "bedrock", e.region, time.Now()); err != nil {
		return nil, err
	}
	return e.base.RoundTrip(signed)
}

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		panic(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     exampleSigV4Transport{credentials: configuration.Credentials, region: configuration.Region, signer: v4.NewSigner(), base: transport},
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	store, err := bedrockkb.NewStore(ctx, bedrockkb.StoreConfig{
		Endpoint: "https://bedrock-agent-runtime.us-east-1.amazonaws.com", HTTPClient: client, KnowledgeBaseID: "KB12345678",
	})
	if err != nil {
		panic(err)
	}
	if _, err = store.Search(ctx, &vectorstore.SearchRequest{Query: "What is AWS?", Options: vectorstore.SearchOptions{TopK: 5}}); err != nil {
		panic(err)
	}
}
