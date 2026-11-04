package azurecosmos

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"

	"github.com/Tangerg/scope/core/document"
)

type selectedItem struct {
	doc  *document.Document
	etag azcore.ETag
}
