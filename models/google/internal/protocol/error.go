package protocol

import (
	"fmt"
	"net/http"

	"github.com/Tangerg/scope/core/httpclient"
)

func newResponseError(cause error, status int, header http.Header) error {
	return fmt.Errorf("%w: %w", httpclient.NewResponseError(status, header), cause)
}
