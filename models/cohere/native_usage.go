package cohere

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"strconv"

	"github.com/Tangerg/scope/core/metadata"
)

// The SDK uses float64 for token counts, which loses integers above 2^53.
// Cohere's number schema also permits integral decimal and exponent notation.
type nativeCount int64

const nativeCountDecimalDigits = 19

func (n *nativeCount) UnmarshalJSON(data []byte) error {
	number := jsontext.Value(data)
	if number.Kind() != jsontext.KindNumber || !number.IsValid() {
		return errors.New("cohere: usage counts must be JSON numbers")
	}
	invalidCountErr := errors.New("cohere: usage counts must be non-negative int64 values")
	mantissa := bytes.TrimSpace(number)
	var exponentText []byte
	if index := bytes.IndexAny(mantissa, "eE"); index >= 0 {
		exponentText = mantissa[index+1:]
		mantissa = mantissa[:index]
	}
	negative := mantissa[0] == '-'
	if negative {
		mantissa = mantissa[1:]
	}
	// Decimal normalization avoids constructing a huge rational number just to
	// reject it; zero does not depend on whether its exponent fits int64.
	significand := bytes.Trim(mantissa, "0.")
	if len(significand) == 0 {
		*n = 0
		return nil
	}
	if negative {
		return invalidCountErr
	}
	trailing := mantissa[len(bytes.TrimRight(mantissa, "0.")):]
	trailingZeros := len(trailing)
	if bytes.IndexByte(trailing, '.') >= 0 {
		trailingZeros--
	}
	digits, fractionalDigits := len(significand), 0
	if point := bytes.IndexByte(mantissa, '.'); point >= 0 {
		fractionalDigits = len(mantissa) - point - 1
	}
	if bytes.IndexByte(significand, '.') >= 0 {
		digits--
	}
	if digits > nativeCountDecimalDigits {
		return invalidCountErr
	}
	var exponent int64
	if len(exponentText) != 0 {
		var err error
		exponent, err = strconv.ParseInt(string(exponentText), 10, 64)
		if err != nil {
			return invalidCountErr
		}
	}
	scale := int64(fractionalDigits - trailingZeros)
	if exponent < scale {
		return invalidCountErr
	}
	// The nonnegative difference can exceed int64 even though both operands fit.
	padding := uint64(exponent) - uint64(scale)
	if padding > uint64(nativeCountDecimalDigits-digits) {
		return invalidCountErr
	}
	var decimal [nativeCountDecimalDigits]byte
	length := 0
	for _, digit := range significand {
		if digit != '.' {
			decimal[length] = digit
			length++
		}
	}
	for range padding {
		decimal[length] = '0'
		length++
	}
	value, err := strconv.ParseInt(string(decimal[:length]), 10, 64)
	if err != nil {
		return invalidCountErr
	}
	*n = nativeCount(value)
	return nil
}

type nativeUsage struct {
	BilledUnits struct {
		InputTokens *nativeCount `json:"input_tokens"`
		SearchUnits *nativeCount `json:"search_units"`
	} `json:"billed_units"`
	Tokens struct {
		InputTokens *nativeCount `json:"input_tokens"`
	} `json:"tokens"`
}

func (n nativeUsage) metadata() (metadata.Map, error) {
	var extra metadata.Map
	if n.BilledUnits.InputTokens != nil {
		if err := extra.Set(BilledInputTokensMetadataKey, int64(*n.BilledUnits.InputTokens)); err != nil {
			return nil, err
		}
	}
	if n.BilledUnits.SearchUnits != nil {
		if err := extra.Set(RerankSearchUnitsMetadataKey, int64(*n.BilledUnits.SearchUnits)); err != nil {
			return nil, err
		}
	}
	return extra, nil
}
