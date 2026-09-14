package chatclient

import (
	"errors"
	"reflect"
	"testing"

	"github.com/Tangerg/scope/core/chat"
)

type recipe struct {
	Name  string   `json:"name"`
	Steps []string `json:"steps"`
}

func TestOutputFormatContracts(t *testing.T) {
	text := Text()
	if err := text.validate(); err != nil || text.contract.Type != chat.OutputFormatText {
		t.Fatalf("Text = (%#v, %v)", text.contract, err)
	}
	jsonFormat := JSON[recipe]()
	if err := jsonFormat.validate(); err != nil || jsonFormat.contract.Type != chat.OutputFormatJSON {
		t.Fatalf("JSON = (%#v, %v)", jsonFormat.contract, err)
	}
	schemaFormat, err := JSONSchema[recipe](JSONSchemaConfig{Name: "recipe", Description: "A recipe"})
	if err != nil {
		t.Fatal(err)
	}
	contract := schemaFormat.contract.Clone()
	if contract.Type != chat.OutputFormatJSONSchema || contract.Name != "recipe" || contract.Schema[0] != '{' {
		t.Fatalf("JSONSchema contract = %#v", contract)
	}
	contract.Schema[0] = '['
	if schemaFormat.contract.Schema[0] != '{' {
		t.Fatal("Contract returned aliased schema bytes")
	}
	if _, err := JSONSchema[recipe](JSONSchemaConfig{}); !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("invalid JSONSchema error = %v", err)
	}
	if _, err := JSONSchema[chan int](JSONSchemaConfig{Name: "invalid"}); !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("unsupported JSONSchema type error = %v", err)
	}
	if err := (OutputFormat[recipe]{}).validate(); !errors.Is(err, ErrInvalidOutputFormat) {
		t.Fatalf("zero OutputFormat error = %v", err)
	}
}

func TestOutputFormatDecodesCompleteLifecycle(t *testing.T) {
	format := JSON[recipe]()
	want := recipe{Name: "tea", Steps: []string{"boil", "steep"}}

	complete := responseWithText(t, `{"name":"tea","steps":["boil","steep"]}`)
	got, err := format.decodeResponse(complete, nil)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode complete = (%#v, %v), want %#v", got, err, want)
	}

}

func TestOutputFormatDecodeStrictJSON(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want recipe
	}{
		{name: "plain", raw: `{"name":"tea","steps":[]}`, want: recipe{Name: "tea", Steps: []string{}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := JSON[recipe]().decodeResponse(responseWithText(t, test.raw), nil)
			if err != nil || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("Decode = (%#v, %v), want %#v", got, err, test.want)
			}
		})
	}
}

func TestOutputFormatDecodeRejectsLossyOrAmbiguousJSON(t *testing.T) {
	for _, raw := range []string{
		"```JSON\n{\"name\":\"tea\",\"steps\":[]}\n```",
		`Here: {"name":"tea","steps":[]} done.`,
		`{"name":"tea","steps":[]`,
		`{"name":"first","steps":[]} {"name":"second","steps":[]}`,
		`{"name":"tea","name":"coffee","steps":[]}`,
		string([]byte{'{', '"', 'n', 'a', 'm', 'e', '"', ':', '"', 0xff, '"', ',', '"', 's', 't', 'e', 'p', 's', '"', ':', '[', ']', '}'}),
	} {
		if _, err := JSON[recipe]().decodeText(raw); !errors.Is(err, ErrInvalidOutput) {
			t.Fatalf("Decode(%q) error = %v, want ErrInvalidOutput", raw, err)
		}
	}
}

func TestOutputFormatDecodeBoundaries(t *testing.T) {
	upstream := errors.New("upstream")
	format := JSON[recipe]()
	if _, err := format.decodeResponse(nil, nil); !errors.Is(err, ErrInvalidOutput) {
		t.Fatalf("nil response error = %v", err)
	}
	if _, err := format.decodeResponse(nil, upstream); !errors.Is(err, upstream) {
		t.Fatalf("upstream error = %v", err)
	}
	if got, err := Text().decodeResponse(responseWithText(t, " exact \n"), nil); err != nil || got != " exact \n" {
		t.Fatalf("text Decode = (%q, %v)", got, err)
	}
}

func responseWithText(t *testing.T, text string) *chat.Response {
	t.Helper()
	message := chat.NewAssistantMessage(chat.NewTextPart(text))
	response, err := chat.NewResponse(&chat.Output{Message: &message, FinishReason: chat.FinishReasonStop}, &chat.ResponseMetadata{})
	if err != nil {
		t.Fatal(err)
	}
	return response
}
