//go:build windows

package network

import (
	"errors"
	"syscall"
	"unsafe"
)

const cryptProtectUIForbidden = 0x1

var (
	crypt32                = syscall.NewLazyDLL("crypt32.dll")
	kernel32               = syscall.NewLazyDLL("kernel32.dll")
	procCryptProtectData   = crypt32.NewProc("CryptProtectData")
	procCryptUnprotectData = crypt32.NewProc("CryptUnprotectData")
	procLocalFree          = kernel32.NewProc("LocalFree")
	dpapiOptionalEntropy   = []byte("BrowserRuntime/network.secret/v1")
)

type dataBlob struct {
	size uint32
	data *byte
}

func newSecretProtector() SecretProtector { return newDPAPIProtector() }

type dpapiProtector struct{}

func newDPAPIProtector() SecretProtector {
	return dpapiProtector{}
}

func (dpapiProtector) Protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("DPAPI input is empty")
	}
	input := blobFromBytes(plain)
	entropy := blobFromBytes(dpapiOptionalEntropy)
	var output dataBlob
	result, _, _ := procCryptProtectData.Call(
		uintptr(unsafe.Pointer(&input)),
		0,
		uintptr(unsafe.Pointer(&entropy)),
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, errors.New("DPAPI protect failed")
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(output.data)))
	return bytesFromBlob(output), nil
}

func (dpapiProtector) Unprotect(cipher []byte) ([]byte, error) {
	if len(cipher) == 0 {
		return nil, errors.New("DPAPI input is empty")
	}
	input := blobFromBytes(cipher)
	entropy := blobFromBytes(dpapiOptionalEntropy)
	var output dataBlob
	result, _, _ := procCryptUnprotectData.Call(
		uintptr(unsafe.Pointer(&input)),
		0,
		uintptr(unsafe.Pointer(&entropy)),
		0,
		0,
		cryptProtectUIForbidden,
		uintptr(unsafe.Pointer(&output)),
	)
	if result == 0 {
		return nil, errors.New("DPAPI unprotect failed")
	}
	defer procLocalFree.Call(uintptr(unsafe.Pointer(output.data)))
	return bytesFromBlob(output), nil
}

func blobFromBytes(value []byte) dataBlob {
	if len(value) == 0 {
		return dataBlob{}
	}
	return dataBlob{size: uint32(len(value)), data: &value[0]}
}

func bytesFromBlob(blob dataBlob) []byte {
	if blob.size == 0 || blob.data == nil {
		return nil
	}
	return append([]byte(nil), unsafe.Slice(blob.data, blob.size)...)
}
