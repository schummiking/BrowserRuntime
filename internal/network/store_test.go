package network

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"browserruntime/internal/profile"
)

type testProtector struct{}

func (testProtector) Protect(plain []byte) ([]byte, error) {
	result := append([]byte("protected:"), plain...)
	for i := len("protected:"); i < len(result); i++ {
		result[i] ^= 0x5a
	}
	return result, nil
}

func (testProtector) Unprotect(cipher []byte) ([]byte, error) {
	if !bytes.HasPrefix(cipher, []byte("protected:")) {
		return nil, errors.New("invalid protected data")
	}
	result := append([]byte(nil), cipher[len("protected:"):]...)
	for i := range result {
		result[i] ^= 0x5a
	}
	return result, nil
}

func createTestProfile(t *testing.T, root, name string) *profile.Profile {
	t.Helper()
	profiles, err := profile.NewStore(root)
	if err != nil {
		t.Fatalf("profile.NewStore() error = %v", err)
	}
	p, err := profiles.Create(name)
	if err != nil {
		t.Fatalf("profiles.Create() error = %v", err)
	}
	return p
}

func newTestStore(t *testing.T, root string) *Store {
	t.Helper()
	store, err := NewStoreWithProtector(root, testProtector{})
	if err != nil {
		t.Fatalf("NewStoreWithProtector() error = %v", err)
	}
	return store
}

func TestDirectPersistence(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Direct")
	store := newTestStore(t, root)

	if err := store.SetDirect(p.ID); err != nil {
		t.Fatalf("SetDirect() error = %v", err)
	}
	got, err := store.Get(p.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Mode != ModeDirect {
		t.Fatalf("Get().Mode = %q, want direct", got.Mode)
	}

	reopened := newTestStore(t, root)
	got, err = reopened.Get(p.ID)
	if err != nil || got.Mode != ModeDirect {
		t.Fatalf("reopened Get() = %#v, %v", got, err)
	}
	if _, err := os.Stat(filepath.Join(root, p.ID, secretFileName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("direct secret file stat error = %v, want not exist", err)
	}
}

func TestProxyPersistenceAndIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	first := createTestProfile(t, root, "First")
	second := createTestProfile(t, root, "Second")
	store := newTestStore(t, root)
	config := Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080, Username: "proxy-user"}
	password := randomTestSecret(t)

	if err := store.SetProxy(first.ID, config, password); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	resolved, err := store.ResolveProxy(first.ID)
	if err != nil {
		t.Fatalf("ResolveProxy() error = %v", err)
	}
	if resolved.Password != password || resolved.Username != config.Username || resolved.Host != config.Host {
		t.Fatalf("ResolveProxy() returned wrong fields")
	}

	metadata, err := os.ReadFile(filepath.Join(root, first.ID, configFileName))
	if err != nil {
		t.Fatalf("ReadFile(network.json) error = %v", err)
	}
	secret, err := os.ReadFile(filepath.Join(root, first.ID, secretFileName))
	if err != nil {
		t.Fatalf("ReadFile(network.secret) error = %v", err)
	}
	if bytes.Contains(metadata, []byte(password)) || bytes.Contains(secret, []byte(password)) {
		t.Fatalf("plaintext password found on disk")
	}
	if bytes.Contains(metadata, []byte("password")) {
		t.Fatalf("network.json contains a password field")
	}
	reopened := newTestStore(t, root)
	reopenedProxy, err := reopened.ResolveProxy(first.ID)
	if err != nil {
		t.Fatalf("reopened ResolveProxy() error = %v", err)
	}
	if reopenedProxy.Password != password || reopenedProxy.Host != config.Host {
		t.Fatal("proxy configuration did not persist across Store recreation")
	}

	if err := store.SetDirect(second.ID); err != nil {
		t.Fatal("explicit initialization failed")
	}
	secondConfig, err := store.Get(second.ID)
	if err != nil || secondConfig.Mode != ModeDirect {
		t.Fatalf("second profile Get() = %#v, %v; want independent direct default", secondConfig, err)
	}
}

func TestMissingOrCorruptSecretFailsClosed(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Fail closed")
	store := newTestStore(t, root)
	config := Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080, Username: "proxy-user"}
	if err := store.SetProxy(p.ID, config, randomTestSecret(t)); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	secretPath := filepath.Join(root, p.ID, secretFileName)
	if err := os.Remove(secretPath); err != nil {
		t.Fatalf("Remove(secret) error = %v", err)
	}
	if _, err := store.ResolveProxy(p.ID); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("ResolveProxy(missing) error = %v, want ErrSecretUnavailable", err)
	}
	if err := os.WriteFile(secretPath, []byte("corrupt"), 0o600); err != nil {
		t.Fatalf("WriteFile(corrupt) error = %v", err)
	}
	if _, err := store.ResolveProxy(p.ID); !errors.Is(err, ErrSecretUnavailable) {
		t.Fatalf("ResolveProxy(corrupt) error = %v, want ErrSecretUnavailable", err)
	}
}

func TestInvalidProfileAndMalformedConfiguration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Invalid")
	store := newTestStore(t, root)
	if err := store.SetDirect("../escape"); !errors.Is(err, profile.ErrInvalidProfileID) {
		t.Fatalf("SetDirect(traversal) error = %v", err)
	}
	if err := store.SetProxy(p.ID, Config{Mode: ModeProxy, Scheme: "unknown", Host: "proxy.example", Port: 1}, "secret"); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("SetProxy(invalid) error = %v", err)
	}
	if err := store.SetProxy(p.ID, Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 1, Username: "user"}, ""); !errors.Is(err, ErrSecretRequired) {
		t.Fatalf("SetProxy(missing password) error = %v", err)
	}
	path := filepath.Join(root, p.ID, configFileName)
	if err := os.WriteFile(path, []byte(`{"mode":"proxy","scheme":"http","host":`), 0o600); err != nil {
		t.Fatalf("WriteFile(malformed) error = %v", err)
	}
	if _, err := store.Get(p.ID); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("Get(malformed) error = %v, want ErrInvalidConfig", err)
	}
}

func TestConnectionDelegatesWithoutLeakingSecret(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Connection")
	store := newTestStore(t, root)
	config := Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080, Username: "proxy-user"}
	password := randomTestSecret(t)
	if err := store.SetProxy(p.ID, config, password); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	tester := &stubConnectionTester{err: errors.New("upstream rejected a request")}
	_, err := store.TestConnection(context.Background(), p.ID, tester)
	if !errors.Is(err, ErrConnectionFailed) {
		t.Fatalf("TestConnection() error = %v, want ErrConnectionFailed", err)
	}
	if strings.Contains(err.Error(), password) || strings.Contains(err.Error(), config.Username) {
		t.Fatalf("TestConnection() error disclosed credentials")
	}
}

type stubConnectionTester struct {
	err error
}

type stubLocalityResolver struct {
	wantExitIP string
	locality   Locality
	err        error
	called     bool
}

func (s *stubLocalityResolver) ResolveLocality(_ context.Context, exitIP string) (Locality, error) {
	s.called = true
	if exitIP != s.wantExitIP {
		return Locality{}, errors.New("unexpected exit IP")
	}
	return s.locality, s.err
}

func TestResolveLocalityPersistsAndUsesCountryLocaleFallback(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Locality")
	store := newTestStore(t, root)
	config := Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080, Username: "proxy-user"}
	if err := store.SetProxy(p.ID, config, randomTestSecret(t)); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	exitIP := "203.0.113.50"
	resolver := &stubLocalityResolver{
		wantExitIP: exitIP,
		locality: Locality{
			ExitIP:      exitIP,
			CountryCode: "US",
			City:        "Los Angeles",
			Timezone:    "America/Los_Angeles",
		},
	}
	locality, err := store.ResolveLocality(context.Background(), p.ID, exitIP, resolver)
	if err != nil {
		t.Fatalf("ResolveLocality() error = %v", err)
	}
	if !resolver.called || locality.Locale != "en-US" {
		t.Fatalf("ResolveLocality() = %#v, resolver called = %v", locality, resolver.called)
	}
	reopened := newTestStore(t, root)
	got, err := reopened.Get(p.ID)
	if err != nil {
		t.Fatalf("reopened Get() error = %v", err)
	}
	if got.Locality == nil || *got.Locality != *locality {
		t.Fatalf("persisted locality = %#v, want %#v", got.Locality, locality)
	}
	metadata, err := os.ReadFile(filepath.Join(root, p.ID, configFileName))
	if err != nil {
		t.Fatalf("ReadFile(network.json) error = %v", err)
	}
	if bytes.Contains(metadata, []byte("password")) {
		t.Fatal("locality persistence introduced a password field")
	}
}

func TestProxyChangeInvalidatesLocality(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Changing proxy")
	store := newTestStore(t, root)
	first := Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "first.example", Port: 8080, Username: "proxy-user"}
	if err := store.SetProxy(p.ID, first, randomTestSecret(t)); err != nil {
		t.Fatalf("SetProxy(first) error = %v", err)
	}
	exitIP := "203.0.113.51"
	resolver := &stubLocalityResolver{wantExitIP: exitIP, locality: Locality{ExitIP: exitIP, CountryCode: "US", City: "Portland", Timezone: "America/Los_Angeles", Locale: "en-US"}}
	if _, err := store.ResolveLocality(context.Background(), p.ID, exitIP, resolver); err != nil {
		t.Fatalf("ResolveLocality() error = %v", err)
	}
	second := Config{
		Mode:     ModeProxy,
		Scheme:   SchemeHTTP,
		Host:     "second.example",
		Port:     8080,
		Username: "proxy-user",
		Locality: &Locality{ExitIP: "203.0.113.200", CountryCode: "US", City: "Injected", Timezone: "America/New_York", Locale: "en-US"},
	}
	if err := store.SetProxy(p.ID, second, randomTestSecret(t)); err != nil {
		t.Fatalf("SetProxy(second) error = %v", err)
	}
	got, err := store.Get(p.ID)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Locality != nil {
		t.Fatalf("Get().Locality = %#v, want unresolved after proxy change", got.Locality)
	}
}

func TestResolveLocalityPrefersResolverLocale(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Explicit locale")
	store := newTestStore(t, root)
	if err := store.SetProxy(p.ID, Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080}, ""); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	exitIP := "203.0.113.53"
	resolver := &stubLocalityResolver{
		wantExitIP: exitIP,
		locality:   Locality{ExitIP: exitIP, CountryCode: "CA", City: "Montreal", Timezone: "America/Toronto", Locale: "fr-CA"},
	}
	locality, err := store.ResolveLocality(context.Background(), p.ID, exitIP, resolver)
	if err != nil {
		t.Fatalf("ResolveLocality() error = %v", err)
	}
	if locality.Locale != "fr-CA" {
		t.Fatalf("ResolveLocality().Locale = %q, want resolver locale fr-CA", locality.Locale)
	}
}

func TestResolveLocalityRejectsInvalidData(t *testing.T) {
	root := filepath.Join(t.TempDir(), "profiles")
	p := createTestProfile(t, root, "Invalid locality")
	store := newTestStore(t, root)
	if err := store.SetProxy(p.ID, Config{Mode: ModeProxy, Scheme: SchemeHTTP, Host: "proxy.example", Port: 8080}, ""); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	exitIP := "203.0.113.52"
	tests := []Locality{
		{ExitIP: exitIP, CountryCode: "US", City: "Seattle", Timezone: "", Locale: "en-US"},
		{ExitIP: exitIP, CountryCode: "US", City: "Seattle", Timezone: "America/Los_Angeles", Locale: "bad_locale"},
		{ExitIP: "203.0.113.99", CountryCode: "US", City: "Seattle", Timezone: "America/Los_Angeles", Locale: "en-US"},
	}
	for _, locality := range tests {
		resolver := &stubLocalityResolver{wantExitIP: exitIP, locality: locality}
		if _, err := store.ResolveLocality(context.Background(), p.ID, exitIP, resolver); !errors.Is(err, ErrInvalidLocality) {
			t.Fatalf("ResolveLocality(%#v) error = %v, want ErrInvalidLocality", locality, err)
		}
	}
}

func (s *stubConnectionTester) TestConnection(context.Context, *ResolvedProxy) (ConnectionResult, error) {
	return ConnectionResult{}, s.err
}
