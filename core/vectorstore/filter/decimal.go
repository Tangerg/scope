package filter

import (
	"cmp"
	"encoding/json"
	"encoding/json/jsontext"
	jsonv2 "encoding/json/v2"
	"math/big"
	"strings"
)

// Decimal position is kept separately from significant digits so a short JSON
// exponent never allocates its expanded power of ten. Trailing zero removal
// makes equal-position magnitudes comparable without padding either operand.
type decimal struct {
	negative bool
	digits   string
	exponent big.Int
}

func parseDecimal(text string) *decimal {
	const numberKind jsontext.Kind = '0'
	wire := jsontext.Value(text)
	if wire.Kind() != numberKind || !wire.IsValid() {
		return nil
	}
	d := &decimal{}
	text = strings.TrimSpace(text)
	text, d.negative = strings.CutPrefix(text, "-")
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		d.exponent.SetString(text[index+1:], 10)
		text = text[:index]
	}
	integer, fraction, _ := strings.Cut(text, ".")
	digits := integer + fraction
	significant := strings.TrimLeft(digits, "0")
	if significant == "" {
		return &decimal{}
	}
	var position big.Int
	position.SetInt64(int64(len(integer) - len(digits) + len(significant)))
	d.exponent.Add(&d.exponent, &position)
	d.digits = strings.TrimRight(significant, "0")
	return d
}

func (d *decimal) sign() int {
	if d.digits == "" {
		return 0
	}
	if d.negative {
		return -1
	}
	return 1
}

func (d *decimal) compare(other *decimal) int {
	if order := cmp.Compare(d.sign(), other.sign()); order != 0 {
		return order
	}
	order := d.exponent.Cmp(&other.exponent)
	if order == 0 {
		order = strings.Compare(d.digits, other.digits)
	}
	if d.negative {
		return -order
	}
	return order
}

// JSON is the shared numeric representation of stored and decoded metadata.
// Values that cannot encode a JSON number remain numeric but unordered.
func numberValue(value any) (*decimal, bool) {
	switch number := value.(type) {
	case json.Number:
		return parseDecimal(number.String()), true
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64:
		wire, err := jsonv2.Marshal(number)
		if err != nil {
			return nil, true
		}
		return parseDecimal(string(wire)), true
	default:
		return nil, false
	}
}
