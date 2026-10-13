//go:build windows || darwin

package app

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	brnetwork "browserruntime/internal/network"
)

type diagnosticFailureStore struct {
	networkStore
	err error
}

func (s diagnosticFailureStore) Get(string) (*brnetwork.Config, error)           { return nil, s.err }
func (s diagnosticFailureStore) SetDirect(string) error                          { return s.err }
func (s diagnosticFailureStore) SetProxy(string, brnetwork.Config, string) error { return s.err }

func TestNetworkApplicationErrorPropagation(t *testing.T) {
	username, password := "BR_USERNAME_MUST_NOT_LEAK", "BR_PASSWORD_MUST_NOT_LEAK"
	s, _, _, _, _ := newTestService(t)
	p, err := s.CreateProfile("Error boundary")
	if err != nil {
		t.Fatal("test setup failed")
	}
	s.networks = diagnosticFailureStore{networkStore: s.networks, err: fmt.Errorf("connection: http://%s:%s@proxy.example.com:8080", username, password)}
	for _, operation := range []func() error{
		func() error { _, err := s.GetNetwork(p.ID); return err },
		func() error { _, err := s.SetDirect(p.ID); return err },
		func() error {
			_, err := s.SetProxy(p.ID, SetProxyInput{Scheme: "http", Host: "proxy.example.com", Port: 8080, Username: username, Password: password})
			return err
		},
		func() error { _, err := s.TestNetwork(p.ID); return err },
		func() error { _, err := s.ListProfiles(); return err },
	} {
		err := operation()
		if err == nil {
			t.Fatal("expected application error")
		}
		if strings.Contains(err.Error(), username) || strings.Contains(err.Error(), password) || strings.Contains(err.Error(), "@") {
			t.Fatal("application propagated raw credential-bearing error")
		}
	}
}

func TestNetworkDiagnosticBoundary(t *testing.T) {
	username, password := "BR_USERNAME_MUST_NOT_LEAK", "BR_PASSWORD_MUST_NOT_LEAK"
	assertSafe := func(output string) {
		t.Helper()
		if strings.Contains(output, username) || strings.Contains(output, password) || strings.Contains(output, "@") {
			t.Fatal("application diagnostic disclosed credentials")
		}
	}
	in := SetProxyInput{Scheme: "http", Host: "proxy.example.com", Port: 8080, Username: username, Password: password}
	view := NetworkView{Mode: "proxy", Scheme: in.Scheme, Host: in.Host, Port: in.Port, Username: username, HasPassword: true}
	for _, output := range []string{fmt.Sprint(in), fmt.Sprintf("%#v", &in), fmt.Sprint(view), fmt.Sprintf("%#v", &view)} {
		assertSafe(output)
		if output != "http://proxy.example.com:8080" {
			t.Fatal("missing safe endpoint context")
		}
	}
	s, _, _, _, _ := newTestService(t)
	p, err := s.CreateProfile("Diagnostic boundary")
	if err != nil {
		t.Fatal("test setup failed")
	}
	in.Port = 0
	_, err = s.SetProxy(p.ID, in)
	if err == nil {
		t.Fatal("expected validation error")
	}
	assertSafe(err.Error())
	for _, cause := range []error{brnetwork.ErrConnectionFailed, brnetwork.ErrInvalidConfig, errors.New("unknown")} {
		wrapped := fmt.Errorf("%w: http://%s:%s@proxy.example.com:8080", cause, username, password)
		assertSafe(safeNetworkError(wrapped).Error())
	}
	in.Port = 8080
	if _, err := s.SetProxy(p.ID, in); err != nil {
		t.Fatal("test setup failed")
	}
	s.tester = connectionStub{err: errors.New(username + ":" + password)}
	result, err := s.TestNetwork(p.ID)
	if err != nil || result == nil || result.Status != "Connection Failed" {
		t.Fatal("failure did not propagate safely")
	}
	assertSafe(result.Status)
	// The normal configuration response keeps username for local editing. Only
	// its diagnostic formatting is credential-free.
	if result.Username != username || !result.HasPassword {
		t.Fatal("normal editing contract changed")
	}
}
