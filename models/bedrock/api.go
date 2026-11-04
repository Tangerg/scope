package bedrock

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/bedrockruntime"

	"github.com/Tangerg/scope/core/httpclient"
)

// Credentials bypasses the ambient AWS credential chain when a caller needs
// an explicit identity, such as a tenant-scoped integration.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

type apiConfig struct {
	Region      string
	BaseURL     string
	HTTPClient  *http.Client
	Credentials *Credentials
}

func (a apiConfig) validate() error {
	if a.Credentials != nil {
		if a.Credentials.AccessKeyID == "" {
			return errors.New("bedrock: Credentials.AccessKeyID is required")
		}
		if a.Credentials.SecretAccessKey == "" {
			return errors.New("bedrock: Credentials.SecretAccessKey is required")
		}
	}
	return nil
}

type api struct {
	client *bedrockruntime.Client
}

func newAPI(ctx context.Context, config apiConfig) (*api, error) {
	if err := config.validate(); err != nil {
		return nil, err
	}

	var loadOptions []func(*awsconfig.LoadOptions) error
	if config.Region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(config.Region))
	}
	if config.HTTPClient != nil {
		loadOptions = append(loadOptions, awsconfig.WithHTTPClient(config.HTTPClient))
	}
	if config.Credentials != nil {
		loadOptions = append(loadOptions, awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			config.Credentials.AccessKeyID,
			config.Credentials.SecretAccessKey,
			config.Credentials.SessionToken,
		)))
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("bedrock: load AWS config: %w", err)
	}

	var bindingErr error
	client := bedrockruntime.NewFromConfig(awsCfg, func(options *bedrockruntime.Options) {
		if config.BaseURL != "" {
			options.BaseEndpoint = aws.String(config.BaseURL)
		}
		httpClient, err := newServiceHTTPClient(ctx, options.HTTPClient, options.BaseEndpoint)
		if err != nil {
			bindingErr = err
			return
		}
		options.HTTPClient = requestBodyClient{client: httpClient}
	})
	if bindingErr != nil {
		return nil, fmt.Errorf("bedrock: bind HTTP client: %w", bindingErr)
	}
	return &api{client: client}, nil
}

func newServiceHTTPClient(ctx context.Context, nativeClient aws.HTTPClient, endpoint *string) (*http.Client, error) {
	if buildable, ok := nativeClient.(*awshttp.BuildableClient); ok {
		// Freeze after SDK initialization retains its resolved transport timeouts.
		nativeClient = buildable.Freeze()
	}
	httpClient, ok := nativeClient.(*http.Client)
	if !ok {
		return nil, fmt.Errorf("unsupported resolved HTTP client %T", nativeClient)
	}
	if endpoint != nil {
		return httpclient.New(ctx, httpclient.Config{
			HTTPClient: httpClient, BaseURL: *endpoint, RejectRedirects: true,
		})
	}
	// The SDK owns regional endpoint resolution, including FIPS aliases.
	// Redirects cannot replace that destination or change the signed operation.
	owned := *httpClient
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &owned, nil
}

type requestBodyClient struct {
	client aws.HTTPClient
}

func (r requestBodyClient) Do(request *http.Request) (*http.Response, error) {
	if request.Body == nil || request.Body == http.NoBody {
		return r.client.Do(request)
	}
	// Smithy's closed WriterTo returns EOF where io.Copy expects nil. The
	// transport can finish copying after response headers arrive, so expose
	// only Read/Close to keep that EOF from closing the live response connection.
	clone := request.Clone(request.Context())
	clone.Body = struct{ io.ReadCloser }{request.Body}
	return r.client.Do(clone)
}

// converseStream returns the event stream rather than the output envelope,
// because that is all a caller does with the output: the envelope carries the
// stream and nothing else a caller reads. It also keeps the surface one the SDK
// can be tested against — an output's stream field is unexported with no
// setter, while an event stream exposes its Reader for exactly that purpose.
func (a *api) converseStream(ctx context.Context, params *bedrockruntime.ConverseStreamInput, opts ...func(*bedrockruntime.Options)) (*bedrockruntime.ConverseStreamEventStream, error) {
	if params == nil {
		return nil, errors.New("bedrock: request must not be nil")
	}
	output, err := a.client.ConverseStream(ctx, params, opts...)
	if err != nil {
		return nil, err
	}
	return output.GetStream(), nil
}

func (a *api) invokeModel(ctx context.Context, params *bedrockruntime.InvokeModelInput, opts ...func(*bedrockruntime.Options)) (*bedrockruntime.InvokeModelOutput, error) {
	if params == nil {
		return nil, errors.New("bedrock: request must not be nil")
	}
	return a.client.InvokeModel(ctx, params, opts...)
}
