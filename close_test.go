//go:build windows || darwin

package main

import (
	"context"
	"errors"
	"testing"
)

func TestCloseGuardBlocksAndNotifies(t *testing.T) {
	for _, blocked := range []bool{true, false} {
		notifications := 0
		guard := closeGuard(func() error {
			if blocked {
				return errors.New("status not stopped")
			}
			return nil
		}, func(context.Context) { notifications++ })
		if guard(context.Background()) != blocked {
			t.Fatal("wrong Wails close result")
		}
		if (notifications == 1) != blocked {
			t.Fatal("close refusal did not notify")
		}
	}
}
