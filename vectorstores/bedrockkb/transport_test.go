package bedrockkb_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/bedrockkb"
)

func TestHostOwnsSigV4Authentication(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/knowledgebases/KB12345678/retrieve" || !strings.Contains(request.Header.Get("Authorization"), "/us-east-1/bedrock/aws4_request") || request.Header.Get("X-Amz-Security-Token") != "test-session" || request.Header.Get("X-Amz-Date") == "" {
			t.Error("native request was not signed by the host")
		}
		writer.Header().Set("Content-Type", "application/json")
		writer.Write([]byte(`{"retrievalResults":[]}`))
	}))
	defer server.Close()
	credentials := aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) {
		return aws.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret", SessionToken: "test-session"}, nil
	})
	client := &http.Client{Transport: exampleSigV4Transport{credentials: credentials, region: "us-east-1", signer: v4.NewSigner(), base: server.Client().Transport}}
	store, err := bedrockkb.NewStore(t.Context(), bedrockkb.StoreConfig{Endpoint: server.URL, HTTPClient: client, KnowledgeBaseID: "KB12345678"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"}); err != nil {
		t.Fatal(err)
	}
	credentialFailure := errors.New("credentials unavailable")
	client.Transport = exampleSigV4Transport{
		credentials: aws.CredentialsProviderFunc(func(_ context.Context) (aws.Credentials, error) { return aws.Credentials{}, credentialFailure }),
		region:      "us-east-1", signer: v4.NewSigner(), base: server.Client().Transport,
	}
	if response, searchErr := store.Search(t.Context(), &vectorstore.SearchRequest{Query: "query"}); response != nil || !errors.Is(searchErr, credentialFailure) {
		t.Fatalf("credential failure became success: %v, %v", response, searchErr)
	}
}
