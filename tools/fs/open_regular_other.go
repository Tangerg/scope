//go:build !unix

package fs

import "os"

const regularReadFlags = os.O_RDONLY
