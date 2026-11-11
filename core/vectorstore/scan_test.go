package vectorstore_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Tangerg/scope/core/vectorstore"
)

func TestScanMeterNeverGrantsTheSameBytesTwice(t *testing.T) {
	meter, err := (vectorstore.ScanBudget{MaxRecords: 9, MaxBytes: 12, Timeout: time.Minute}).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer meter.Close()
	var accepted atomic.Int32
	var wg sync.WaitGroup
	for range 15 {
		wg.Go(func() {
			chargeErr := meter.Charge(3)
			if chargeErr == nil {
				accepted.Add(1)
			} else if !errors.Is(chargeErr, vectorstore.ErrScanBudget) {
				t.Error(chargeErr)
			}
		})
	}
	wg.Wait()
	if accepted.Load() != 4 {
		t.Fatalf("accepted %d records", accepted.Load())
	}
	if err = meter.Charge(0); err != nil {
		t.Fatal("rejected bytes consumed remaining record grant", err)
	}
	meter.Close()
	if err = meter.Charge(0); err == nil {
		t.Fatal("closed deadline still allows reads")
	}
}

func TestScanMeterReportsAnElapsedGrantAsTheBudget(t *testing.T) {
	meter, err := (vectorstore.ScanBudget{MaxRecords: 1, MaxBytes: 1, Timeout: time.Nanosecond}).Begin(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	defer meter.Close()
	<-meter.Context().Done()
	if err := meter.Charge(0); !errors.Is(err, vectorstore.ErrScanBudget) {
		t.Fatalf("Charge after the grant elapsed = %v, want ErrScanBudget", err)
	}
	if cause := context.Cause(meter.Context()); !errors.Is(cause, vectorstore.ErrScanBudget) {
		t.Fatalf("meter cause = %v, want ErrScanBudget", cause)
	}
}
