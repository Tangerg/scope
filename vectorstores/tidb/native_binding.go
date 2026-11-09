package tidb

import (
	"fmt"

	"github.com/Tangerg/scope/core/vectorstore"
)

const nativeTextCharset = "utf8mb4"

type nativeTextType string

const (
	nativeTinyText   nativeTextType = "tinytext"
	nativeText       nativeTextType = "text"
	nativeMediumText nativeTextType = "mediumtext"
	nativeLongText   nativeTextType = "longtext"
)

func (n nativeTextType) byteCapacity() (int64, error) {
	// TiDB multiplies TEXT's byte limit by the charset width in
	// CHARACTER_OCTET_LENGTH; the native type owns the actual byte limit.
	const (
		tinyTextByteCapacity   int64 = 255
		textByteCapacity       int64 = 65535
		mediumTextByteCapacity int64 = 16777215
		longTextByteCapacity   int64 = 4294967295
	)
	switch n {
	case nativeTinyText:
		return tinyTextByteCapacity, nil
	case nativeText:
		return textByteCapacity, nil
	case nativeMediumText:
		return mediumTextByteCapacity, nil
	case nativeLongText:
		return longTextByteCapacity, nil
	default:
		return 0, fmt.Errorf("tidb: content column type %q is not a native TEXT type", n)
	}
}

type nativeBinding struct {
	dimensions   int
	contentBytes int64
}

func (n nativeBinding) checkContent(text string) error {
	if int64(len(text)) > n.contentBytes {
		return fmt.Errorf("%w: tidb content requires %d bytes; native column permits %d", vectorstore.ErrInvalidDocument, len(text), n.contentBytes)
	}
	return nil
}
