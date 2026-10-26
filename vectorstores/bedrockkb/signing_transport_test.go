package bedrockkb_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
)

type signingRequestBody struct {
	readError  error
	closeError error
	closed     int
}

func (s *signingRequestBody) Read([]byte) (int, error) {
	if s.readError != nil {
		return 0, s.readError
	}
	return 0, io.EOF
}

func (s *signingRequestBody) Close() error {
	s.closed++
	return s.closeError
}

func TestSigningTransportClosesRejectedRequestBody(t *testing.T) {
	cause := errors.New("signing input unavailable")
	closeFailure := errors.New("request body close failed")
	for _, stage := range []string{"replay missing", "replay creation", "payload read", "credentials"} {
		t.Run(stage, func(t *testing.T) {
			request, err := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://bedrock.example/retrieve", strings.NewReader("payload"))
			if err != nil {
				t.Fatal(err)
			}
			body := &signingRequestBody{closeError: closeFailure}
			request.Body = body
			transport := exampleSigV4Transport{
				credentials: aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
					return aws.Credentials{}, cause
				}),
				signer: v4.NewSigner(),
				region: "us-east-1",
			}
			switch stage {
			case "replay missing":
				request.GetBody = nil
			case "replay creation":
				request.GetBody = func() (io.ReadCloser, error) { return nil, cause }
			case "payload read":
				request.GetBody = func() (io.ReadCloser, error) { return &signingRequestBody{readError: cause}, nil }
			}
			response, err := transport.RoundTrip(request)
			if response != nil || body.closed != 1 || !errors.Is(err, closeFailure) {
				t.Fatalf("signing failure leaked the request body: response=%v closed=%d error=%v", response, body.closed, err)
			}
			if stage != "replay missing" && !errors.Is(err, cause) {
				t.Fatalf("signing cause lost: %v", err)
			}
		})
	}
}
