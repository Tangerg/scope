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
		assertWrongPassword(t)
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
	// The bounded child first proves termination; this call then contributes
	// the same password boundary to the parent coverage profile.
	assertWrongPassword(t)
}

func assertWrongPassword(t *testing.T) {
	t.Helper()
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
}

func TestEncryptedDocumentsRequireTheCorrectPassword(t *testing.T) {
	// These ReportLab pages were encrypted with pypdf using RC4-128 and AES-128.
	// Real encryption dictionaries exercise the successful decryption path too.
	for _, path := range []string{"testdata/encrypted-rc4.pdf", "testdata/encrypted-aes128.pdf"} {
		t.Run(path, func(t *testing.T) {
			payload, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, password := range []string{"", "wrong", "scope-password"} {
				for _, perPage := range []bool{false, true} {
					reader, err := pdf.NewReader(bytes.NewReader(payload), int64(len(payload)), pdf.ReaderConfig{
						Password: password,
						PerPage:  perPage,
					})
					if err != nil {
						t.Fatal(err)
					}
					docs, err := reader.Read(t.Context())
					if password != "scope-password" {
						if len(docs) != 0 || !errors.Is(err, ledongthuc.ErrInvalidPassword) {
							t.Fatalf("password=%q perPage=%t Read = %v, %v", password, perPage, docs, err)
						}
						continue
					}
					if err != nil {
						t.Fatal(err)
					}
					want := []string{"First page\n\nSecond page"}
					if perPage {
						want = []string{"First page", "Second page"}
					}
					if len(docs) != len(want) {
						t.Fatalf("perPage=%t documents = %v", perPage, docs)
					}
					for index, doc := range docs {
						if doc.Text != want[index] {
							t.Fatalf("perPage=%t page=%d text = %q, want %q", perPage, index+1, doc.Text, want[index])
						}
						assertMetadata(t, doc.Metadata, pdf.MetadataPagesTotal, 2)
						if perPage {
							assertMetadata(t, doc.Metadata, pdf.MetadataPageIndex, index+1)
						}
					}
				}
			}
		})
	}
}
