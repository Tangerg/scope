package httpreq_test

import (
	"fmt"
	"net/url"

	"github.com/Tangerg/scope/tools/httpreq"
)

func ExampleNewAllowlist() {
	allowlist, err := httpreq.NewAllowlist([]string{"api.example.com", "*.services.example.com", "localhost:8080"})
	if err != nil {
		panic(err)
	}

	for _, target := range []string{
		"https://API.EXAMPLE.COM/v1",
		"https://search.services.example.com/",
		"https://api.example.com:8443/",
		"http://localhost:8080/health",
		"https://example.net/",
	} {
		parsed, err := url.Parse(target)
		if err != nil {
			panic(err)
		}
		fmt.Println(target, allowlist.Allows(parsed))
	}
	// Output:
	// https://API.EXAMPLE.COM/v1 true
	// https://search.services.example.com/ true
	// https://api.example.com:8443/ false
	// http://localhost:8080/health true
	// https://example.net/ false
}
