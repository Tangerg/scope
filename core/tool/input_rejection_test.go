package tool_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/Tangerg/scope/core/tool"
)

func TestInputRejectionKeepsPublicCorrectionSeparateFromDiagnosticEvidence(t *testing.T) {
	rejection, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: "count must be even", Cause: context.Canceled})
	if err != nil {
		t.Fatal(err)
	}
	if rejection.Error() != "count must be even" || rejection.Message() != rejection.Error() || !errors.Is(rejection.Cause(), context.Canceled) {
		t.Fatal("public correction or diagnostic was lost")
	}
	if !errors.Is(rejection, tool.ErrInvalidInvocation) || errors.Is(rejection, context.Canceled) {
		t.Fatal("diagnostic cause changed the rejection identity")
	}
	if _, definite := tool.ClassifyFailure(rejection); definite {
		t.Fatal("input rejection claimed an execution outcome")
	}
	foreign, err := tool.NewFailure(tool.FailureConfig{Kind: tool.FailureKindFailed, Cause: rejection, Output: tool.NewTextOutput("foreign outcome")})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name string
		err  error
		want bool
	}{
		{"direct", rejection, true},
		{"wrapped", fmt.Errorf("private wrapper: %w", rejection), true},
		{"single joined", errors.Join(rejection), true},
		{"independent errors", errors.Join(rejection, errors.New("private diagnostic")), false},
		{"foreign outcome cause", foreign, false},
		{"ordinary error", errors.New("input is invalid"), false},
		{"invalid rejection", new(tool.InputRejection), false},
		{"typed nil", (*tool.InputRejection)(nil), false},
		{"nil", nil, false},
	} {
		t.Run(row.name, func(t *testing.T) {
			got, found := tool.ClassifyInputRejection(row.err)
			if found != row.want || (found && got != rejection) {
				t.Fatalf("classification = %v, %v", got, found)
			}
		})
	}
}

func TestInputRejectionRejectsUnpublishableMessagesAndTypedNilCauses(t *testing.T) {
	for _, config := range []tool.InputRejectionConfig{
		{},
		{Message: " \n "},
		{Message: string([]byte{0xff})},
		{Message: "correct input", Cause: (*tool.InputRejection)(nil)},
	} {
		if rejection, err := tool.NewInputRejection(config); rejection != nil || !errors.Is(err, tool.ErrInvalidInputRejection) {
			t.Fatalf("invalid rejection admitted: %v, %v", rejection, err)
		}
	}
	var absent *tool.InputRejection
	if absent.Message() != "" || absent.Cause() != nil || absent.Error() != tool.ErrInvalidInputRejection.Error() || absent.Is(tool.ErrInvalidInvocation) {
		t.Fatal("nil rejection established public facts")
	}
	if new(tool.InputRejection).Error() != tool.ErrInvalidInputRejection.Error() {
		t.Fatal("zero rejection established a message")
	}
	var defect *tool.InputValidationError
	if defect.Cause() != nil {
		t.Fatal("nil validation error established a diagnostic")
	}
}

func TestPrepareDistinguishesCallerInputFromHostAdmissionAndValidatorDefects(t *testing.T) {
	public, err := tool.NewInputRejection(tool.InputRejectionConfig{Message: "use an even count", Cause: errors.New("private validation detail")})
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range []struct {
		name       string
		request    tool.Request
		admission  tool.Admission
		validation error
		wantPublic bool
	}{
		{name: "request identity", request: tool.Request{}, wantPublic: true},
		{name: "wrong tool", request: tool.Request{Name: "other"}, wantPublic: true},
		{name: "schema", request: tool.Request{Name: "marked", Arguments: `[]`}, wantPublic: true},
		{name: "explicit validator rejection", request: tool.Request{Name: "marked"}, validation: fmt.Errorf("private wrapper: %w", public), wantPublic: true},
		{name: "invalid host key", request: tool.Request{Name: "marked"}, admission: tool.Admission{IdempotencyKey: tool.IdempotencyKey(string([]byte{0xff}))}},
		{name: "invalid host continuation", request: tool.Request{Name: "marked"}, admission: tool.Admission{Continuation: new(tool.Continuation)}},
		{name: "validator diagnostic", request: tool.Request{Name: "marked"}, validation: errors.New("private validator failure")},
		{name: "ambiguous validator", request: tool.Request{Name: "marked"}, validation: errors.Join(public, errors.New("private side failure"))},
		{name: "cancellation diagnostic", request: tool.Request{Name: "marked"}, validation: context.Canceled},
	} {
		t.Run(row.name, func(t *testing.T) {
			binding, err := tool.Bind(validatingTool{declare: func() func([]byte) error {
				return func([]byte) error { return row.validation }
			}})
			if err != nil {
				t.Fatal(err)
			}
			_, err = binding.Contract().Prepare(row.request, row.admission)
			rejection, found := tool.ClassifyInputRejection(err)
			if !errors.Is(err, tool.ErrInvalidInvocation) || found != row.wantPublic {
				t.Fatalf("Prepare = %v, public=%v", err, found)
			}
			if found && rejection.Message() == "" {
				t.Fatal("correctable input has no public correction")
			}
			if row.validation != nil && !row.wantPublic {
				defect, found := errors.AsType[*tool.InputValidationError](err)
				if !found || !errors.Is(defect.Cause(), row.validation) || errors.Is(err, row.validation) {
					t.Fatal("validator diagnostic escaped or lost its private cause", err)
				}
			}
		})
	}
	if _, err := (tool.Contract{}).Prepare(tool.Request{Name: "marked"}, tool.Admission{}); err == nil {
		t.Fatal("zero contract admitted input")
	} else if _, public := tool.ClassifyInputRejection(err); public {
		t.Fatal("host contract defect became input correction")
	}
}
