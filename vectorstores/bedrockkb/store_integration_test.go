//go:build integration

package bedrockkb_test

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/bedrockkb"
)

func TestNativeKnowledgeBaseRetrieve(t *testing.T) {
	region := os.Getenv("AWS_REGION")
	endpoint := os.Getenv("SCOPE_BEDROCK_KB_ENDPOINT")
	knowledgeBaseID := os.Getenv("SCOPE_BEDROCK_KB_ID")
	if region == "" || endpoint == "" || knowledgeBaseID == "" {
		t.Fatal("AWS_REGION, SCOPE_BEDROCK_KB_ENDPOINT and SCOPE_BEDROCK_KB_ID are required for the native integration test")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	native, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(region))
	if err != nil {
		t.Fatal(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	client := &http.Client{
		Transport:     exampleSigV4Transport{credentials: native.Credentials, region: native.Region, signer: v4.NewSigner(), base: transport},
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	store, err := bedrockkb.NewStore(ctx, bedrockkb.StoreConfig{Endpoint: endpoint, HTTPClient: client, KnowledgeBaseID: knowledgeBaseID})
	if err != nil {
		t.Fatal(err)
	}
	request := &vectorstore.SearchRequest{Query: "What is contained in this knowledge base?", Options: vectorstore.SearchOptions{TopK: 5}}
	response, err := store.Search(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	if err = response.ValidateFor(request); err != nil {
		t.Fatal(err)
	}
	if len(response.Results) == 0 {
		t.Fatal("the integration knowledge base must contain ingested text documents")
	}
}
