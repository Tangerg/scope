package s3vectors_test

import (
	"context"
	"net/http"

	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awss3 "github.com/aws/aws-sdk-go-v2/service/s3vectors"

	"github.com/Tangerg/scope/core/document"

	"github.com/Tangerg/scope/core/vectorstore"
	"github.com/Tangerg/scope/vectorstores/s3vectors"
)

func ExampleNewStore() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	configuration, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion("us-east-1"))
	if err != nil {
		panic(err)
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	defer transport.CloseIdleConnections()
	configuration.HTTPClient = &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	client := awss3.NewFromConfig(configuration)
	const space vectorstore.SpaceID = "encoder-v1"
	store, err := s3vectors.NewStore(ctx, s3vectors.StoreConfig{Client: client, VectorBucketName: "example-bucket", IndexName: "documents", Space: space})
	if err != nil {
		panic(err)
	}
	if err = store.Upsert(ctx, &vectorstore.Batch{Space: space, Records: []vectorstore.Record{{Document: &document.Document{ID: "one", Text: "example"}, Values: []float64{1, 0}}}}); err != nil {
		panic(err)
	}
	if _, err = store.Query(ctx, &vectorstore.Query{Space: space, Values: []float64{1, 0}}); err != nil {
		panic(err)
	}
	if err = store.DeleteIDs(ctx, []string{"one"}); err != nil {
		panic(err)
	}
}
