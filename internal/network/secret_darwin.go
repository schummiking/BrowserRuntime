package network

/*
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <Security/Security.h>
#include <string.h>
#include <stdlib.h>
static OSStatus br_key(const char *serviceName, unsigned char *candidate, unsigned char *result) {
    CFStringRef service=CFStringCreateWithCString(NULL,serviceName,kCFStringEncodingUTF8);
    const void *keys[] = {kSecClass,kSecAttrService,kSecAttrAccount,kSecReturnData};
    const void *values[] = {kSecClassGenericPassword,service,CFSTR("v1"),kCFBooleanTrue};
    CFDictionaryRef query=CFDictionaryCreate(NULL,keys,values,4,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
    CFTypeRef data=NULL;
    OSStatus status=SecItemCopyMatching(query,&data);
    if (status==errSecItemNotFound) {
        CFDataRef key=CFDataCreate(NULL,candidate,32);
        const void *addKeys[]={kSecClass,kSecAttrService,kSecAttrAccount,kSecValueData};
        const void *addValues[]={kSecClassGenericPassword,service,CFSTR("v1"),key};
        CFDictionaryRef add=CFDictionaryCreate(NULL,addKeys,addValues,4,&kCFTypeDictionaryKeyCallBacks,&kCFTypeDictionaryValueCallBacks);
        status=SecItemAdd(add,NULL);
        CFRelease(add); CFRelease(key);
        if (status==errSecSuccess || status==errSecDuplicateItem) status=SecItemCopyMatching(query,&data);
    }
    if (status==errSecSuccess) {
        if (CFGetTypeID(data)!=CFDataGetTypeID() || CFDataGetLength((CFDataRef)data)!=32) status=errSecDecode;
        else memcpy(result,CFDataGetBytePtr((CFDataRef)data),32);
    }
    if (data) CFRelease(data);
    CFRelease(query); CFRelease(service);
    return status;
}
*/
import "C"

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"errors"
	"sync"
	"unsafe"
)

type keychainProtector struct {
	service string
	mu      sync.Mutex
	aead    cipher.AEAD
}

func newSecretProtector() SecretProtector {
	return &keychainProtector{service: "com.schummiking.browserruntime.proxy-key"}
}

func (p *keychainProtector) encryption() (cipher.AEAD, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.aead != nil {
		return p.aead, nil
	}
	candidate := make([]byte, 32)
	key := make([]byte, 32)
	defer clear(candidate)
	defer clear(key)
	if _, err := rand.Read(candidate); err != nil {
		return nil, err
	}
	service := C.CString(p.service)
	defer C.free(unsafe.Pointer(service))
	if C.br_key(service, (*C.uchar)(unsafe.Pointer(&candidate[0])), (*C.uchar)(unsafe.Pointer(&key[0]))) != 0 {
		return nil, errors.New("macOS Keychain is unavailable or access was denied")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	p.aead, err = cipher.NewGCM(block)
	return p.aead, err
}

func (p *keychainProtector) Protect(plain []byte) ([]byte, error) {
	if len(plain) == 0 {
		return nil, errors.New("secret is empty")
	}
	a, err := p.encryption()
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, a.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	return a.Seal(nonce, nonce, plain, []byte("BrowserRuntime/network.secret/v1")), nil
}
func (p *keychainProtector) Unprotect(data []byte) ([]byte, error) {
	a, err := p.encryption()
	if err != nil {
		return nil, err
	}
	if len(data) < a.NonceSize()+a.Overhead() {
		return nil, errors.New("invalid encrypted secret")
	}
	return a.Open(nil, data[:a.NonceSize()], data[a.NonceSize():], []byte("BrowserRuntime/network.secret/v1"))
}
