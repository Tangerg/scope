package bedrock_test

import (
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/Tangerg/scope/core/embedding"
	"github.com/Tangerg/scope/models/bedrock"
)

func TestHTTPBindingPreservesSDKEndpointResolution(t *testing.T) {
	for _, test := range []struct {
		name, region, endpoint string
		fips, dualStack        bool
	}{
		{name: "canonical region", region: "us-east-1"},
		{name: "FIPS region alias", region: "fips-us-east-1"},
		{name: "FIPS suffix alias", region: "us-east-1-fips"},
		{name: "FIPS and dual stack", region: "us-east-1", fips: true, dualStack: true},
		{name: "environment endpoint", region: "us-east-1", endpoint: "https://configured.example.test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("AWS_ENDPOINT_URL", "")
			t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", test.endpoint)
			t.Setenv("AWS_USE_FIPS_ENDPOINT", strconv.FormatBool(test.fips))
			t.Setenv("AWS_USE_DUALSTACK_ENDPOINT", strconv.FormatBool(test.dualStack))
			reference := &endpointCaptureTransport{}
			config, err := awsconfig.LoadDefaultConfig(t.Context(),
				awsconfig.WithRegion(test.region),
				awsconfig.WithHTTPClient(&http.Client{Transport: reference}),
				awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider("test-key", "test-secret", "")),
			)
			if err != nil {
				t.Fatal(err)
			}
			sdk := bedrockruntime.NewFromConfig(config)
			_, err = sdk.InvokeModel(t.Context(), &bedrockruntime.InvokeModelInput{
				ModelId: aws.String("amazon.titan-embed-text-v2:0"), ContentType: aws.String("application/json"),
				Body: []byte(`{"inputText":"hello"}`),
			})
			if err != nil {
				t.Fatal(err)
			}
			actual := &endpointCaptureTransport{}
			client := &http.Client{Transport: actual}
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
				Region: test.region, HTTPClient: client,
				Credentials:    &bedrock.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret"},
				DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"},
			})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"hello"}}); err != nil {
				t.Fatal(err)
			}
			if len(reference.urls) != 1 || len(actual.urls) != 1 || actual.urls[0] != reference.urls[0] {
				t.Fatalf("Scope URLs=%v, SDK URLs=%v", actual.urls, reference.urls)
			}
			if client.Transport != actual || client.CheckRedirect != nil {
				t.Fatal("host HTTP client was mutated")
			}
		})
	}
}

func TestDefaultSDKEndpointCannotRedirectTheSignedRequest(t *testing.T) {
	t.Setenv("AWS_ENDPOINT_URL", "")
	t.Setenv("AWS_ENDPOINT_URL_BEDROCK_RUNTIME", "")
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			transport := &endpointCaptureTransport{redirectStatus: status}
			client := &http.Client{Transport: transport}
			model, err := bedrock.NewEmbeddingModel(t.Context(), bedrock.EmbeddingModelConfig{
				Region: "us-east-1", HTTPClient: client,
				Credentials:    &bedrock.Credentials{AccessKeyID: "test-key", SecretAccessKey: "test-secret", SessionToken: "test-token"},
				DefaultOptions: embedding.Options{Model: "amazon.titan-embed-text-v2:0"},
			})
			if err != nil {
				t.Fatal(err)
			}
			response, err := model.Call(t.Context(), &embedding.Request{Texts: []string{"hello"}})
			if response != nil || err == nil || len(transport.urls) == 0 {
				t.Fatalf("response=%v error=%v requests=%v", response, err, transport.urls)
			}
			for _, endpoint := range transport.urls {
				if strings.HasPrefix(endpoint, "https://other.example.test/") {
					t.Fatalf("signed request followed redirect: %v", transport.urls)
				}
			}
			if client.Transport != transport || client.CheckRedirect != nil {
				t.Fatal("host HTTP client was mutated")
			}
		})
	}
}

type endpointCaptureTransport struct {
	urls           []string
	redirectStatus int
}

func (e *endpointCaptureTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Body != nil {
		defer request.Body.Close()
	}
	e.urls = append(e.urls, request.URL.String())
	response := &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(strings.NewReader(`{"embedding":[1,2],"inputTextTokenCount":1}`)),
		Request:    request,
	}
	if e.redirectStatus != 0 {
		response.StatusCode = e.redirectStatus
		response.Header.Set("Location", "https://other.example.test/model")
	}
	return response, nil
}
