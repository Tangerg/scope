package ollama

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"math"
	"math/big"
	"time"
)

const indefiniteKeepAlive = time.Duration(-1)

var errKeepAliveDurationRange = errors.New("ollama: keep_alive seconds exceed the duration range")

type nativeDuration struct {
	time.Duration
}

func (n nativeDuration) MarshalJSON() ([]byte, error) {
	if n.Duration < 0 {
		return jsonv2.Marshal(int64(indefiniteKeepAlive))
	}
	return jsonv2.Marshal(n.String())
}

func (n *nativeDuration) UnmarshalJSON(data []byte) error {
	var value any
	if err := jsonv2.Unmarshal(data, &value); err != nil {
		return err
	}
	switch typed := value.(type) {
	case float64:
		number := bytes.TrimSpace(data)
		mantissa := number
		if exponent := bytes.IndexAny(number, "eE"); exponent >= 0 {
			mantissa = number[:exponent]
		}
		// A negative nonzero JSON number may underflow to -0 in float64.
		// Only the mantissa determines whether the caller requested infinity.
		if !bytes.ContainsAny(mantissa, "123456789") {
			n.Duration = 0
			return nil
		}
		if mantissa[0] == '-' {
			n.Duration = indefiniteKeepAlive
			return nil
		}
		if typed < float64(time.Nanosecond)/float64(time.Second) {
			n.Duration = 0
			return nil
		}
		if typed > float64(math.MaxInt64)/float64(time.Second) {
			return errKeepAliveDurationRange
		}
		// JSON seconds need exact arithmetic at the int64 boundary: float64
		// rounds MaxInt64 upward, so a float comparison cannot admit it safely.
		seconds, ok := new(big.Rat).SetString(string(number))
		if !ok {
			return errors.New("ollama: invalid numeric duration")
		}
		seconds.Mul(seconds, big.NewRat(int64(time.Second), 1))
		nanoseconds := new(big.Int).Quo(seconds.Num(), seconds.Denom())
		if !nanoseconds.IsInt64() {
			return errKeepAliveDurationRange
		}
		n.Duration = time.Duration(nanoseconds.Int64())
		return nil
	case string:
		parsed, err := time.ParseDuration(typed)
		if err != nil {
			return err
		}
		if parsed < 0 {
			parsed = indefiniteKeepAlive
		}
		n.Duration = parsed
		return nil
	default:
		return errors.New("ollama: duration must be a number of seconds or duration string")
	}
}
