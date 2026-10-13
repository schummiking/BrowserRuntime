//go:build windows

package processmanager

import (
	"errors"
	"testing"
)

func TestWindowsNamedMutexRejectsSecondHolder(t *testing.T) {
	provider := windowsMutexLeaseProvider{}
	first, err := provider.Acquire(validTestProfileID)
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	defer first.Release()

	if _, err := provider.Acquire(validTestProfileID); !errors.Is(err, ErrProfileAlreadyRunning) {
		t.Fatalf("Acquire(second) error = %v, want ErrProfileAlreadyRunning", err)
	}
	first.Release()
	second, err := provider.Acquire(validTestProfileID)
	if err != nil {
		t.Fatalf("Acquire(after release) error = %v", err)
	}
	second.Release()
}
