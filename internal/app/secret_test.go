//go:build windows || darwin

package app

import (
	"bytes"
	"errors"
)

// Business tests use synthetic secrets; OS encryption is tested in network.
type testSecretProtector struct{}

func (testSecretProtector) Protect(plain []byte) ([]byte, error) {
	result := append([]byte("test:"), plain...)
	for i := 5; i < len(result); i++ {
		result[i] ^= 0x5a
	}
	return result, nil
}

func (testSecretProtector) Unprotect(data []byte) ([]byte, error) {
	if !bytes.HasPrefix(data, []byte("test:")) {
		return nil, errors.New("invalid test secret")
	}
	result := append([]byte(nil), data[5:]...)
	for i := range result {
		result[i] ^= 0x5a
	}
	return result, nil
}
