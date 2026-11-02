package run

import (
	"cmp"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/Tangerg/scope/agent/internal/jsonwire"
)

const maxIdentityBytes = 256

var ErrInvalidIdentity = errors.New("run: invalid identity")

type identity struct{ value string }

func parseIdentity(kind, value string) (identity, error) {
	if !validIdentity(value) {
		return identity{}, fmt.Errorf("%w: %s must contain 1 to %d URI-safe characters", ErrInvalidIdentity, kind, maxIdentityBytes)
	}
	return identity{value: value}, nil
}

func validIdentity(value string) bool {
	if len(value) == 0 || len(value) > maxIdentityBytes || value == "." || value == ".." {
		return false
	}
	for _, c := range value {
		if c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_' || c == '.' || c == ':' {
			continue
		}
		return false
	}
	return true
}

func (i identity) String() string { return i.value }
func (i identity) Valid() bool    { return i.value != "" }
func (i identity) IsZero() bool   { return !i.Valid() }
func (i identity) MarshalText() ([]byte, error) {
	if !i.Valid() {
		return nil, ErrInvalidIdentity
	}
	return []byte(i.value), nil
}

func (identity) JSONSchemaModel() any { return "" }

type ProcessID struct{ path string }

func ParseProcessID(value string) (ProcessID, error) {
	parts := strings.Split(value, "/")
	for _, part := range parts {
		if !validIdentity(part) {
			return ProcessID{}, ErrInvalidIdentity
		}
	}
	return ProcessID{path: value}, nil
}

func (p ProcessID) String() string { return p.path }

// compare orders paths lexically, which places every parent before its children.
func (p ProcessID) compare(other ProcessID) int { return strings.Compare(p.path, other.path) }
func (p ProcessID) Valid() bool                 { return p.path != "" }
func (p ProcessID) IsZero() bool                { return !p.Valid() }
func (p ProcessID) Root() ProcessID {
	root, _, _ := strings.Cut(p.path, "/")
	return ProcessID{path: root}
}

func (p ProcessID) Parent() (ProcessID, bool) {
	index := strings.LastIndexByte(p.path, '/')
	if index < 0 {
		return ProcessID{}, false
	}
	return ProcessID{path: p.path[:index]}, true
}

func (p ProcessID) Key() (ChildKey, bool) {
	index := strings.LastIndexByte(p.path, '/')
	if index < 0 {
		return ChildKey{}, false
	}
	return ChildKey{identity{p.path[index+1:]}}, true
}

func (p ProcessID) Depth() uint32 { return uint32(strings.Count(p.path, "/")) }
func (p ProcessID) Child(key ChildKey) (ProcessID, error) {
	if !p.Valid() || !key.Valid() {
		return ProcessID{}, ErrInvalidIdentity
	}
	return ProcessID{path: p.path + "/" + key.String()}, nil
}

func (p ProcessID) Contains(other ProcessID) bool {
	for other.Valid() {
		if p == other {
			return true
		}
		parent, ok := other.Parent()
		if !ok {
			return false
		}
		other = parent
	}
	return false
}

func (p ProcessID) MarshalText() ([]byte, error) {
	if !p.Valid() {
		return nil, ErrInvalidIdentity
	}
	return []byte(p.path), nil
}

func (p *ProcessID) UnmarshalText(data []byte) error {
	if p == nil {
		return ErrInvalidIdentity
	}
	value, err := ParseProcessID(string(data))
	if err == nil {
		*p = value
	}
	return err
}

func (ProcessID) JSONSchemaModel() any { return "" }

type SignalID struct{ identity }

func ParseSignalID(value string) (SignalID, error) {
	id, err := parseIdentity("signal ID", value)
	return SignalID{id}, err
}

func (s *SignalID) UnmarshalText(data []byte) error {
	if s == nil {
		return ErrInvalidIdentity
	}
	value, err := ParseSignalID(string(data))
	if err == nil {
		*s = value
	}
	return err
}

type WaitKey struct{ identity }

func ParseWaitKey(value string) (WaitKey, error) {
	id, err := parseIdentity("wait key", value)
	return WaitKey{id}, err
}

func (w *WaitKey) UnmarshalText(data []byte) error {
	if w == nil {
		return ErrInvalidIdentity
	}
	value, err := ParseWaitKey(string(data))
	if err == nil {
		*w = value
	}
	return err
}

type ChildKey struct{ identity }

func ParseChildKey(value string) (ChildKey, error) {
	id, err := parseIdentity("child key", value)
	return ChildKey{id}, err
}

func (c *ChildKey) UnmarshalText(data []byte) error {
	if c == nil {
		return ErrInvalidIdentity
	}
	value, err := ParseChildKey(string(data))
	if err == nil {
		*c = value
	}
	return err
}

type EffectID struct {
	Process ProcessID `json:"process"`
	Step    uint64    `json:"step"`
	Index   uint32    `json:"index" jsonwire:"required"`
}

func (e EffectID) Valid() bool  { return e.Process.Valid() && e.Step > 0 }
func (e EffectID) IsZero() bool { return e == EffectID{} }

// compare orders Effects by process, then by declaration within the process.
func (e EffectID) compare(other EffectID) int {
	return cmp.Or(e.Process.compare(other.Process), cmp.Compare(e.Step, other.Step), cmp.Compare(e.Index, other.Index))
}

// Precedes reports whether e was declared before other by the same process.
// Effects of different processes have no declaration order.
func (e EffectID) Precedes(other EffectID) bool {
	if e.Process != other.Process {
		return false
	}
	return e.Step < other.Step || e.Step == other.Step && e.Index < other.Index
}
func (e EffectID) String() string {
	if !e.Valid() {
		return ""
	}
	return e.Process.String() + "/" + strconv.FormatUint(e.Step, 10) + "/" + strconv.FormatUint(uint64(e.Index), 10)
}

func ParseEffectID(value string) (EffectID, error) {
	parts := strings.Split(value, "/")
	if len(parts) < 3 {
		return EffectID{}, ErrInvalidIdentity
	}
	process, err := ParseProcessID(strings.Join(parts[:len(parts)-2], "/"))
	if err != nil {
		return EffectID{}, err
	}
	step, err := strconv.ParseUint(parts[len(parts)-2], 10, 64)
	if err != nil || step == 0 {
		return EffectID{}, ErrInvalidIdentity
	}
	index, err := strconv.ParseUint(parts[len(parts)-1], 10, 32)
	if err != nil {
		return EffectID{}, ErrInvalidIdentity
	}
	id := EffectID{Process: process, Step: step, Index: uint32(index)}
	if id.String() != value {
		return EffectID{}, ErrInvalidIdentity
	}
	return id, nil
}

func (e EffectID) MarshalJSON() ([]byte, error) {
	if !e.Valid() {
		return nil, ErrInvalidIdentity
	}
	return jsonv2.Marshal(effectIDWire(e))
}

func (e *EffectID) UnmarshalJSON(data []byte) error {
	if e == nil {
		return ErrInvalidIdentity
	}
	value, err := jsonwire.Decode[effectIDWire](data)
	if err != nil {
		return err
	}
	candidate := EffectID(value)
	if !candidate.Valid() {
		return ErrInvalidIdentity
	}
	*e = candidate
	return nil
}

func (EffectID) JSONSchemaModel() any { return effectIDWire{} }

type effectIDWire EffectID

type Epoch uint64
type LSN uint64
