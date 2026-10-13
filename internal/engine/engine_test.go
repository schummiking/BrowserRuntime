package engine

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestValidateLaunchSpecRejectsForbiddenAutomationAndCDPFlags(t *testing.T) {
	forbidden := []string{
		"--remote-debugging-port=9222",
		"--remote-debugging-pipe",
		"--remote-debugging-address=127.0.0.1",
		"--enable-automation",
		"--ENABLE-AUTOMATION=true",
	}
	for _, argument := range forbidden {
		t.Run(argument, func(t *testing.T) {
			spec := LaunchSpec{ExecutablePath: filepath.Join(t.TempDir(), "chrome"), Arguments: []string{argument}}
			if err := ValidateLaunchSpec(spec); !errors.Is(err, ErrForbiddenLaunchArgument) {
				t.Fatalf("ValidateLaunchSpec(%q) error = %v, want ErrForbiddenLaunchArgument", argument, err)
			}
		})
	}
}

func TestValidateLaunchSpecAcceptsNativeArguments(t *testing.T) {
	spec := LaunchSpec{
		ExecutablePath: filepath.Join(t.TempDir(), "chrome"),
		Arguments: []string{
			`--user-data-dir=C:\profiles\br_0123456789abcdef0123456789abcdef\browser-data`,
			"--fingerprint=12345",
		},
	}
	if err := ValidateLaunchSpec(spec); err != nil {
		t.Fatalf("ValidateLaunchSpec() error = %v", err)
	}
}
