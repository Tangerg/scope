package azurecosmos

import (
	"testing"

	"github.com/Tangerg/scope/core/history/storetest"
)

func TestStoreConformance(t *testing.T) {
	storetest.Run(t, new(Store), storetest.Capabilities{
		Reader: true, Writer: true, Clearer: true, Lister: true,
	})
}
