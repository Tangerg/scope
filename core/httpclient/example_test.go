package httpclient_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/httpclient"
)

func Example() {
	ctx := context.Background()
	host := &http.Client{}
	client, err := httpclient.New(ctx, httpclient.Config{
		HTTPClient: host,
		BaseURL:    "https://api.example/v1",
	})
	if err != nil {
		panic(err)
	}
	redirect, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.example:9443/result", nil)
	if err != nil {
		panic(err)
	}
	err = client.CheckRedirect(redirect, nil)
	fmt.Println("independent client:", client != host)
	fmt.Println("host transport unchanged:", host.Transport == nil)
	fmt.Println("other port rejected:", errors.Is(err, httpclient.ErrOriginNotAllowed))
	// Output:
	// independent client: true
	// host transport unchanged: true
	// other port rejected: true
}
