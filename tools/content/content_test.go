package content_test

import (
	"bytes"
	jsonv2 "encoding/json/v2"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/tools/content"
)

func TestContentHasOneLosslessCanonicalRepresentation(t *testing.T) {
	for _, test := range []struct {
		name string
		data []byte
		wire string
		text bool
	}{
		{name: "empty", wire: `{"encoding":"utf8","data":""}`, text: true},
		{name: "text", data: []byte("你好"), wire: `{"encoding":"utf8","data":"你好"}`, text: true},
		{name: "binary", data: []byte{0xff, 0x00}, wire: `{"encoding":"base64","data":"/wA="}`},
		{name: "incomplete UTF8", data: []byte{0xe4, 0xbd}, wire: `{"encoding":"base64","data":"5L0="}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := content.New(test.data)
			encoded, err := jsonv2.Marshal(value)
			if err != nil || string(encoded) != test.wire {
				t.Fatalf("encoded content = %s, %v; want %s", encoded, err, test.wire)
			}
			text, valid := value.Text()
			if valid != test.text || valid && text != string(test.data) {
				t.Fatalf("Text = %q, %t", text, valid)
			}
			var decoded content.Content
			if err := jsonv2.Unmarshal(encoded, &decoded); err != nil || !bytes.Equal(decoded.Bytes(), test.data) {
				t.Fatalf("decoded content = %x, %v; want %x", decoded.Bytes(), err, test.data)
			}
		})
	}
}

func TestContentOwnsItsBytes(t *testing.T) {
	input := []byte{0xff, 0x01}
	value := content.New(input)
	input[0] = 0
	observed := value.Bytes()
	observed[1] = 0
	if !bytes.Equal(value.Bytes(), []byte{0xff, 0x01}) {
		t.Fatal("content shares mutable byte ownership")
	}
}

func TestContentRejectsInvalidOrNoncanonicalWireWithoutMutation(t *testing.T) {
	for _, wire := range []string{
		`null`, `{}`, `"old string shape"`, `{"encoding":"utf8"}`, `{"data":""}`,
		`{"encoding":"utf8","data":null}`, `{"encoding":"utf8","data":"","extra":true}`,
		`{"encoding":"utf8","data":"","data":"again"}`, `{"encoding":"hex","data":"ff"}`,
		`{"encoding":"base64","data":"/w"}`, `{"encoding":"base64","data":"/x=="}`,
		`{"encoding":"base64","data":"/w==\n"}`, `{"encoding":"base64","data":"aGk="}`,
	} {
		value := content.New([]byte("retained"))
		// The JSON decoder may reject syntax before invoking Content's decoder.
		if err := jsonv2.Unmarshal([]byte(wire), &value); err == nil {
			t.Fatalf("invalid content %s returned %v", wire, err)
		}
		if !bytes.Equal(value.Bytes(), []byte("retained")) {
			t.Fatalf("invalid content %s changed receiver", wire)
		}
	}
	var absent *content.Content
	if err := absent.UnmarshalJSON([]byte(`{}`)); !errors.Is(err, content.ErrInvalidContent) {
		t.Fatalf("nil Content receiver = %v", err)
	}
}

func ExampleContent() {
	value := content.New([]byte{0xff, 0x00})
	encoded, err := jsonv2.Marshal(value)
	if err != nil {
		panic(err)
	}
	fmt.Println(string(encoded))
	fmt.Printf("%x\n", value.Bytes())
	// Output:
	// {"encoding":"base64","data":"/wA="}
	// ff00
}
