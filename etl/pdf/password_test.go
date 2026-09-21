package pdf_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	ledongthuc "github.com/ledongthuc/pdf"

	"github.com/Tangerg/scope/etl/pdf"
)

func TestWrongPDFPasswordTerminates(t *testing.T) {
	if os.Getenv("SCOPE_TEST_PDF_PASSWORD_CHILD") == "1" {
		var data bytes.Buffer
		data.WriteString("%PDF-1.4\n")
		offset := data.Len()
		fmt.Fprintf(&data, "xref\n0 1\n0000000000 65535 f \ntrailer\n<< /Size 1 /ID [(scope-test)] /Encrypt << /Filter /Standard /V 1 /R 2 /Length 40 /O <%s> /U <%s> /P 0 >> >>\nstartxref\n%d\n%%%%EOF\n", strings.Repeat("00", 32), strings.Repeat("00", 32), offset)
		reader, err := pdf.NewReader(bytes.NewReader(data.Bytes()), int64(data.Len()), pdf.ReaderConfig{Password: "incorrect"})
		if err != nil {
			t.Fatal(err)
		}
		_, err = reader.Read(t.Context())
		if !errors.Is(err, ledongthuc.ErrInvalidPassword) {
			t.Fatalf("password error = %v", err)
		}
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestWrongPDFPasswordTerminates$")
	command.Env = append(os.Environ(), "SCOPE_TEST_PDF_PASSWORD_CHILD=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("password read did not terminate: %v\n%s", err, output)
	}
}
