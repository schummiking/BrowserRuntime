package network

import (
	"bytes"
	"fmt"
	"os/exec"
	"testing"
	"time"
)

func TestKeychainSecretRoundTripAndTamper(t *testing.T) {
	service := fmt.Sprintf("com.schummiking.browserruntime.test.%d", time.Now().UnixNano())
	p := &keychainProtector{service: service}
	t.Cleanup(func() {
		_ = exec.Command("/usr/bin/security", "delete-generic-password", "-s", service, "-a", "v1").Run()
	})
	plain := []byte("synthetic-macos-proxy-secret")
	encrypted, err := p.Protect(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encrypted, plain) {
		t.Fatal("plaintext persisted")
	}
	// A fresh protector retrieves the same Keychain key after manager restart.
	restored, err := (&keychainProtector{service: service}).Unprotect(encrypted)
	if err != nil || !bytes.Equal(restored, plain) {
		t.Fatal("secret did not survive protector restart")
	}
	encrypted[len(encrypted)-1] ^= 1
	if _, err = p.Unprotect(encrypted); err == nil {
		t.Fatal("accepted tampered ciphertext")
	}
	if _, err = p.Unprotect([]byte("invalid")); err == nil {
		t.Fatal("accepted truncated ciphertext")
	}
}
