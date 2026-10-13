//go:build windows || darwin

package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/profile"
)

type connectionStub struct {
	result brnetwork.ConnectionResult
	err    error
}

func (c connectionStub) TestConnection(context.Context, *brnetwork.ResolvedProxy) (brnetwork.ConnectionResult, error) {
	return c.result, c.err
}

func proxyInput() SetProxyInput {
	return SetProxyInput{Scheme: "http", Host: "proxy.example", Port: 8080, Username: "test-user", Password: "synthetic-first-secret"}
}

func TestNetworkSecretBoundaryAndPasswordEditing(t *testing.T) {
	s, _, store, _, _ := newTestService(t)
	p, err := s.CreateProfile("Network")
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.GetNetwork(p.ID)
	if err != nil || v.Mode != "direct" || v.Status != "Direct" || v.HasPassword {
		t.Fatal("incorrect direct view")
	}
	in := proxyInput()
	v, err = s.SetProxy(p.ID, in)
	if err != nil || !v.HasPassword || v.Status != "Needs Test" {
		t.Fatal("incorrect proxy view")
	}
	data, err := json.Marshal(v)
	if err != nil || strings.Contains(string(data), in.Password) || strings.Contains(string(data), `"password"`) {
		t.Fatal("secret crossed DTO boundary")
	}
	in.Password = ""
	in.Host = "replacement.example"
	if _, err := s.SetProxy(p.ID, in); err != nil {
		t.Fatal(err)
	}
	resolved, err := store.ResolveProxy(p.ID)
	if err != nil || resolved.Password != "synthetic-first-secret" {
		t.Fatal("empty replacement did not retain secret")
	}
	in.Password = "synthetic-replacement-secret"
	if _, err := s.SetProxy(p.ID, in); err != nil {
		t.Fatal(err)
	}
	resolved, err = store.ResolveProxy(p.ID)
	if err != nil || resolved.Password != in.Password {
		t.Fatal("replacement did not persist")
	}
	if _, err := s.SetDirect(p.ID); err != nil {
		t.Fatal(err)
	}
	in.Password = ""
	if _, err := s.SetProxy(p.ID, in); !errors.Is(err, brnetwork.ErrSecretRequired) {
		t.Fatal("missing password not rejected")
	}
}

func TestNetworkReadinessTransitionsAndFailClosed(t *testing.T) {
	s, _, store, pm, adapter := newTestService(t)
	p, err := s.CreateProfile("Readiness")
	if err != nil {
		t.Fatal(err)
	}
	views, err := s.ListProfiles()
	if err != nil || !views[0].LaunchReady {
		t.Fatal("direct must be launch ready")
	}
	if _, err := s.SetProxy(p.ID, proxyInput()); err != nil {
		t.Fatal(err)
	}
	views, _ = s.ListProfiles()
	if views[0].LaunchReady || views[0].Network != "Needs Test" || views[0].LaunchBlockedReason != "Test network before launch" {
		t.Fatal("unresolved proxy must be blocked")
	}
	if err := s.LaunchProfile(p.ID); err == nil || len(pm.launched) != 0 || adapter.directCalls != 0 {
		t.Fatal("unresolved launch must fail closed")
	}
	s.tester = connectionStub{result: brnetwork.ConnectionResult{OK: true, ExitIP: "203.0.113.80"}}
	s.resolver = fixedResolver{locality: brnetwork.Locality{ExitIP: "203.0.113.80", CountryCode: "US", City: "Los Angeles", Timezone: "America/Los_Angeles", Locale: "en-US"}}
	v, err := s.TestNetwork(p.ID)
	if err != nil || v.Status != "Ready" || v.Locality == nil {
		t.Fatal("successful test must resolve locality")
	}
	views, _ = s.ListProfiles()
	if !views[0].LaunchReady || views[0].Network != "US · Los Angeles" {
		t.Fatal("resolved proxy must be ready")
	}
	s.tester = connectionStub{err: errors.New("synthetic-provider-secret-response")}
	v, err = s.TestNetwork(p.ID)
	if err != nil || v.Status != "Connection Failed" || v.Locality != nil {
		t.Fatal("failure state incorrect")
	}
	views, _ = s.ListProfiles()
	if views[0].LaunchReady || views[0].Network != "Connection Failed" {
		t.Fatal("failed proxy must be blocked")
	}
	if err := s.LaunchProfile(p.ID); err == nil || len(pm.launched) != 0 {
		t.Fatal("failed launch must fail closed")
	}
	config, err := store.Get(p.ID)
	if err != nil || config.Locality != nil {
		t.Fatal("failed test retained stale persisted locality")
	}
	delete(s.networkFailures, p.ID) // A new app instance must still block launch.
	views, _ = s.ListProfiles()
	if views[0].LaunchReady {
		t.Fatal("restart must not restore stale readiness")
	}
}

func TestProxyEditInvalidatesLocalityAndRunningEditRejected(t *testing.T) {
	s, _, store, pm, _ := newTestService(t)
	p, _ := s.CreateProfile("Edit")
	if _, err := s.SetProxy(p.ID, proxyInput()); err != nil {
		t.Fatal(err)
	}
	locality := brnetwork.Locality{ExitIP: "203.0.113.80", CountryCode: "US", Timezone: "America/Los_Angeles", Locale: "en-US"}
	if _, err := store.ResolveLocality(context.Background(), p.ID, locality.ExitIP, fixedResolver{locality}); err != nil {
		t.Fatal(err)
	}
	in := proxyInput()
	in.Password = ""
	in.Port++
	v, err := s.SetProxy(p.ID, in)
	if err != nil || v.Status != "Needs Test" || v.Locality != nil {
		t.Fatal("edit must invalidate locality")
	}
	pm.statuses[p.ID] = "RUNNING"
	if _, err := s.SetDirect(p.ID); err == nil {
		t.Fatal("running network edit not rejected")
	}
}

func TestValidationNeverLeaks(t *testing.T) {
	s, _, _, _, _ := newTestService(t)
	p, _ := s.CreateProfile("Secret")
	in := proxyInput()
	if _, err := s.SetProxy(p.ID, in); err != nil {
		t.Fatal(err)
	}
	in.Scheme = "unsupported"
	_, err := s.SetProxy(p.ID, in)
	if err == nil || strings.Contains(err.Error(), in.Password) {
		t.Fatal("validation error is not safe")
	}
}

func TestMissingOrCorruptSecretBlocksReadinessAndLaunch(t *testing.T) {
	for _, missing := range []bool{true, false} {
		t.Run(map[bool]string{true: "missing", false: "corrupt"}[missing], func(t *testing.T) {
			root := t.TempDir()
			profiles, err := profile.NewStore(root)
			if err != nil {
				t.Fatal(err)
			}
			networks, err := brnetwork.NewStoreWithProtector(root, testSecretProtector{})
			if err != nil {
				t.Fatal(err)
			}
			pm := newFakeProcessManager()
			s, err := NewService(profiles, networks, &fakeAdapter{}, pm, brnetwork.StartLoopbackBridge, connectionStub{}, fixedResolver{})
			if err != nil {
				t.Fatal(err)
			}
			p, _ := s.CreateProfile("Secret unavailable")
			if _, err := s.SetProxy(p.ID, proxyInput()); err != nil {
				t.Fatal(err)
			}
			locality := brnetwork.Locality{ExitIP: "203.0.113.80", CountryCode: "US", Timezone: "America/Los_Angeles", Locale: "en-US"}
			if _, err := networks.ResolveLocality(context.Background(), p.ID, locality.ExitIP, fixedResolver{locality}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, p.ID, "network.secret")
			if missing {
				err = os.Remove(path)
			} else {
				err = os.WriteFile(path, []byte("invalid DPAPI blob"), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			view, err := s.GetNetwork(p.ID)
			if err != nil || view.HasPassword || view.Status != "Needs Test" || view.Locality != nil {
				t.Fatal("unusable secret advertised as ready")
			}
			views, err := s.ListProfiles()
			if err != nil || views[0].LaunchReady {
				t.Fatal("unusable secret must block readiness")
			}
			if err := s.LaunchProfile(p.ID); err == nil || len(pm.launched) != 0 {
				t.Fatal("unusable secret must fail closed")
			}
			in := proxyInput()
			in.Password = ""
			in.Host = "changed.example"
			if _, err := s.SetProxy(p.ID, in); !errors.Is(err, brnetwork.ErrSecretUnavailable) {
				t.Fatal("unavailable retained secret not rejected")
			}
			in.Password = "synthetic-new-secret"
			if _, err := s.SetProxy(p.ID, in); err != nil {
				t.Fatal("explicit secret replacement must repair configuration")
			}
		})
	}
}
