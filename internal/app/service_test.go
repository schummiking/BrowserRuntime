//go:build windows || darwin

package app

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"browserruntime/internal/engine"
	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/processmanager"
	"browserruntime/internal/profile"
)

type fakeProcessManager struct {
	statuses map[string]processmanager.Status
	launched []string
	opened   []string
	stopped  []string
}

func newFakeProcessManager() *fakeProcessManager {
	return &fakeProcessManager{statuses: make(map[string]processmanager.Status)}
}

func (m *fakeProcessManager) HasActiveProfiles() bool {
	for _, status := range m.statuses {
		if status != processmanager.StatusStopped {
			return true
		}
	}
	return false
}

func (m *fakeProcessManager) Launch(profileID string, _ engine.LaunchSpec) error {
	if m.statuses[profileID] == processmanager.StatusRunning {
		return processmanager.ErrProfileAlreadyRunning
	}
	m.statuses[profileID] = processmanager.StatusRunning
	m.launched = append(m.launched, profileID)
	return nil
}

func (m *fakeProcessManager) LaunchPrepared(id string, prepare func() (engine.LaunchSpec, error)) error {
	status, err := m.Status(id)
	if err != nil {
		return err
	}
	if status != processmanager.StatusStopped {
		return processmanager.OccupancyError(status)
	}
	spec, err := prepare()
	if err != nil {
		return err
	}
	return m.Launch(id, spec)
}

func (m *fakeProcessManager) WithStoppedProfiles(ids []string, action func() error) error {
	for _, id := range ids {
		status, err := m.Status(id)
		if err != nil {
			return err
		}
		if status != processmanager.StatusStopped {
			return processmanager.OccupancyError(status)
		}
	}
	return action()
}

func (m *fakeProcessManager) Open(profileID string) error {
	if m.statuses[profileID] != processmanager.StatusRunning {
		return processmanager.ErrProfileNotRunning
	}
	m.opened = append(m.opened, profileID)
	return nil
}

func (m *fakeProcessManager) Stop(profileID string) error {
	if m.statuses[profileID] != processmanager.StatusRunning {
		return processmanager.ErrProfileNotRunning
	}
	m.statuses[profileID] = processmanager.StatusStopped
	m.stopped = append(m.stopped, profileID)
	return nil
}

func (m *fakeProcessManager) Status(profileID string) (processmanager.Status, error) {
	status := m.statuses[profileID]
	if status == "" {
		status = processmanager.StatusStopped
	}
	return status, nil
}

type fakeAdapter struct {
	directCalls int
	proxyCalls  int
}

func (a *fakeAdapter) LaunchSpec(profile.Profile) (engine.LaunchSpec, error) {
	a.directCalls++
	return engine.LaunchSpec{ExecutablePath: `C:\test\cloak.exe`}, nil
}

func (a *fakeAdapter) LaunchSpecWithNetwork(profile.Profile, brnetwork.EngineProxy) (engine.LaunchSpec, error) {
	a.proxyCalls++
	return engine.LaunchSpec{ExecutablePath: `C:\test\cloak.exe`}, nil
}

type fixedResolver struct {
	locality brnetwork.Locality
}

func (r fixedResolver) ResolveLocality(context.Context, string) (brnetwork.Locality, error) {
	return r.locality, nil
}

func newTestService(t *testing.T) (*Service, *profile.Store, *brnetwork.Store, *fakeProcessManager, *fakeAdapter) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "profiles")
	profiles, err := profile.NewStore(root)
	if err != nil {
		t.Fatalf("profile.NewStore() error = %v", err)
	}
	networks, err := brnetwork.NewStoreWithProtector(root, testSecretProtector{})
	if err != nil {
		t.Fatalf("network.NewStore() error = %v", err)
	}
	processes := newFakeProcessManager()
	adapter := &fakeAdapter{}
	service, err := NewService(profiles, networks, adapter, processes, brnetwork.StartLoopbackBridge, connectionStub{}, fixedResolver{})
	if err != nil {
		t.Fatalf("NewService() error = %v", err)
	}
	return service, profiles, networks, processes, adapter
}

func TestListProfileViewsAndNetworkLabels(t *testing.T) {
	service, profiles, networks, processes, _ := newTestService(t)
	direct, err := profiles.Create("Direct profile")
	if err != nil {
		t.Fatalf("Create(direct) error = %v", err)
	}
	if err := networks.SetDirect(direct.ID); err != nil {
		t.Fatalf("SetDirect() error = %v", err)
	}
	proxied, err := profiles.Create("Proxy profile")
	if err != nil {
		t.Fatalf("Create(proxy) error = %v", err)
	}
	if err := networks.SetProxy(proxied.ID, brnetwork.Config{Mode: brnetwork.ModeProxy, Scheme: brnetwork.SchemeHTTP, Host: "proxy.example", Port: 8080}, ""); err != nil {
		t.Fatalf("SetProxy() error = %v", err)
	}
	exitIP := "203.0.113.80"
	locality := brnetwork.Locality{ExitIP: exitIP, CountryCode: "US", City: "Los Angeles", Timezone: "America/Los_Angeles", Locale: "en-US"}
	if _, err := networks.ResolveLocality(context.Background(), proxied.ID, exitIP, fixedResolver{locality: locality}); err != nil {
		t.Fatalf("ResolveLocality() error = %v", err)
	}
	processes.statuses[proxied.ID] = processmanager.StatusRunning

	views, err := service.ListProfiles()
	if err != nil {
		t.Fatalf("ListProfiles() error = %v", err)
	}
	if len(views) != 2 {
		t.Fatalf("ListProfiles() length = %d, want 2", len(views))
	}
	byID := map[string]ProfileView{views[0].ID: views[0], views[1].ID: views[1]}
	if got := byID[direct.ID]; got.Name != "Direct profile" || got.Browser != "CloakBrowser" || got.Network != "Direct" || got.Status != "Stopped" {
		t.Fatalf("direct ProfileView = %#v", got)
	}
	if got := byID[proxied.ID]; got.Network != "US · Los Angeles" || got.Status != "Running" {
		t.Fatalf("proxy ProfileView = %#v", got)
	}
}

func TestCreateRenameAndDeleteStoppedProfile(t *testing.T) {
	service, _, networks, _, _ := newTestService(t)
	created, err := service.CreateProfile("  Work  ")
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	if created.Name != "Work" || created.Network != "Direct" || created.Status != "Stopped" {
		t.Fatalf("CreateProfile() = %#v", created)
	}
	config, err := networks.Get(created.ID)
	if err != nil || config.Mode != brnetwork.ModeDirect {
		t.Fatalf("created network = %#v, %v", config, err)
	}
	renamed, err := service.RenameProfile(created.ID, "Personal")
	if err != nil || renamed.Name != "Personal" {
		t.Fatalf("RenameProfile() = %#v, %v", renamed, err)
	}
	if err := service.DeleteProfile(created.ID); err != nil {
		t.Fatalf("DeleteProfile() error = %v", err)
	}
	if _, err := service.RenameProfile(created.ID, "Gone"); !errors.Is(err, profile.ErrProfileNotFound) {
		t.Fatalf("RenameProfile(deleted) error = %v", err)
	}
}

func TestRunningDeleteRejected(t *testing.T) {
	service, _, _, processes, _ := newTestService(t)
	created, err := service.CreateProfile("Running")
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	processes.statuses[created.ID] = processmanager.StatusRunning
	if err := service.DeleteProfile(created.ID); !errors.Is(err, ErrRunningProfileDelete) {
		t.Fatalf("DeleteProfile(running) error = %v", err)
	}
}

func TestLifecycleDelegation(t *testing.T) {
	service, _, _, processes, adapter := newTestService(t)
	created, err := service.CreateProfile("Lifecycle")
	if err != nil {
		t.Fatalf("CreateProfile() error = %v", err)
	}
	if err := service.LaunchProfile(created.ID); err != nil {
		t.Fatalf("LaunchProfile() error = %v", err)
	}
	if adapter.directCalls != 1 || len(processes.launched) != 1 {
		t.Fatalf("launch delegation adapter/process = %d/%d", adapter.directCalls, len(processes.launched))
	}
	if err := service.OpenProfile(created.ID); err != nil || len(processes.opened) != 1 {
		t.Fatalf("OpenProfile() error/calls = %v/%d", err, len(processes.opened))
	}
	if err := service.StopProfile(created.ID); err != nil || len(processes.stopped) != 1 {
		t.Fatalf("StopProfile() error/calls = %v/%d", err, len(processes.stopped))
	}
}
