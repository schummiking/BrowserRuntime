//go:build windows || darwin

package main

import "context"

// The same Wails lifecycle handles both the titlebar X and Alt+F4.
func closeGuard(prepare func() error, notify func(context.Context)) func(context.Context) bool {
	return func(ctx context.Context) bool {
		if prepare() != nil {
			notify(ctx)
			return true
		}
		return false
	}
}
