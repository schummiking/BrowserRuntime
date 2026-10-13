//go:build windows || darwin

package app

import (
	"crypto/sha256"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"browserruntime/internal/engine"
	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/processmanager"
	"browserruntime/internal/profile"
)

// These acceptance probes use temporary profiles, DPAPI, local bridge listeners
// and fake browser processes. They never launch a browser or contact a proxy.
func acceptanceService(t *testing.T) (*Service, string, *fakeProcessManager) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "profiles")
	profiles, err := profile.NewStore(root)
	if err != nil {
		t.Fatal("create temporary profile store")
	}
	networks, err := brnetwork.NewStoreWithProtector(root, testSecretProtector{})
	if err != nil {
		t.Fatal("create temporary network store")
	}
	pm := newFakeProcessManager()
	s, err := NewService(profiles, networks, &fakeAdapter{}, pm, brnetwork.StartLoopbackBridge,
		connectionStub{result: brnetwork.ConnectionResult{OK: true, ExitIP: "203.0.113.80"}},
		fixedResolver{locality: brnetwork.Locality{ExitIP: "203.0.113.80", CountryCode: "US", City: "Los Angeles", Timezone: "America/Los_Angeles", Locale: "en-US"}})
	if err != nil {
		t.Fatal("create temporary service")
	}
	t.Cleanup(func() {
		for id := range s.bridges {
			s.closeBridge(id, s.currentBridge(id))
		}
	})
	return s, root, pm
}

func acceptanceProxy(t *testing.T, s *Service, name string) string {
	t.Helper()
	p, err := s.CreateProfile(name)
	if err != nil {
		t.Fatal("create temporary profile")
	}
	_, err = s.SetProxy(p.ID, SetProxyInput{Scheme: "http", Host: "127.0.0.1", Port: 1, Username: "BR_USERNAME_MUST_NOT_LEAK", Password: "BR_PASSWORD_MUST_NOT_LEAK"})
	if err != nil {
		t.Fatal("save synthetic proxy")
	}
	v, err := s.TestNetwork(p.ID)
	if err != nil || v.Status != "Ready" {
		t.Fatal("mock locality setup failed")
	}
	return p.ID
}

type acceptanceFile struct {
	hash     [32]byte
	modified time.Time
}

func acceptanceNetworkFiles(t *testing.T, root, id string) [2]acceptanceFile {
	t.Helper()
	var result [2]acceptanceFile
	for i, name := range []string{"network.json", "network.secret"} {
		path := filepath.Join(root, id, name)
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal("read temporary network file")
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal("stat temporary network file")
		}
		result[i] = acceptanceFile{sha256.Sum256(data), info.ModTime()}
	}
	return result
}

func TestAcceptanceActiveNetworkMutationsLeaveFilesUnchanged(t *testing.T) {
	for _, status := range []processmanager.Status{processmanager.StatusStarting, processmanager.StatusRunning, processmanager.StatusStopping} {
		t.Run(string(status), func(t *testing.T) {
			s, root, pm := acceptanceService(t)
			id := acceptanceProxy(t, s, "Mutation guard")
			before := acceptanceNetworkFiles(t, root, id)
			pm.statuses[id] = status
			for _, mutate := range []func() error{
				func() error { _, err := s.SetDirect(id); return err },
				func() error {
					_, err := s.SetProxy(id, SetProxyInput{Scheme: "http", Host: "replacement.example", Port: 8080})
					return err
				},
				func() error { _, err := s.TestNetwork(id); return err },
			} {
				if mutate() == nil {
					t.Fatal("active network mutation accepted")
				}
				if before != acceptanceNetworkFiles(t, root, id) {
					t.Fatal("rejected mutation changed persisted network/locality/secret")
				}
			}
			pm.statuses[id] = processmanager.StatusStopped
			if _, err := s.SetDirect(id); err != nil {
				t.Fatal("stopped editing rejected")
			}
		})
	}
}

type acceptanceStopManager struct {
	*fakeProcessManager
	timeout         bool
	stopped, resume chan struct{}
}

func (p *acceptanceStopManager) Stop(id string) error {
	if p.timeout {
		return processmanager.ErrGracefulStopTimeout
	}
	if err := p.fakeProcessManager.Stop(id); err != nil {
		return err
	}
	if p.stopped != nil {
		close(p.stopped)
		<-p.resume
	}
	return nil
}

func acceptanceBridgeClosed(b *brnetwork.LoopbackBridge) bool {
	select {
	case <-b.Done():
		return true
	default:
		return false
	}
}

func acceptanceWaitBridgeClosed(t *testing.T, b *brnetwork.LoopbackBridge) {
	t.Helper()
	select {
	case <-b.Done():
	case <-time.After(time.Second):
		t.Fatal("bridge listener did not close")
	}
}

func TestAcceptanceBridgeIsolationTimeoutStopAndManualClose(t *testing.T) {
	s, _, pm := acceptanceService(t)
	first := acceptanceProxy(t, s, "First")
	second := acceptanceProxy(t, s, "Second")
	if s.LaunchProfile(first) != nil || s.LaunchProfile(second) != nil {
		t.Fatal("mock launch failed")
	}
	a, b := s.bridges[first], s.bridges[second]
	if a == nil || b == nil || a == b || a.EngineProxy().Port == b.EngineProxy().Port {
		t.Fatal("profiles share a bridge")
	}
	if acceptanceBridgeClosed(a) || acceptanceBridgeClosed(b) {
		t.Fatal("running bridge stopped")
	}
	if !errors.Is(s.LaunchProfile(first), processmanager.ErrProfileAlreadyRunning) {
		t.Fatal("duplicate launch accepted")
	}
	s.processes = &acceptanceStopManager{fakeProcessManager: pm, timeout: true}
	if !errors.Is(s.StopProfile(first), processmanager.ErrGracefulStopTimeout) {
		t.Fatal("timeout not reported")
	}
	if s.bridges[first] != a || acceptanceBridgeClosed(a) {
		t.Fatal("timeout released running bridge")
	}
	s.processes = pm
	if err := s.StopProfile(first); err != nil {
		t.Fatal("stop failed")
	}
	acceptanceWaitBridgeClosed(t, a)
	if acceptanceBridgeClosed(b) {
		t.Fatal("stop affected another profile")
	}
	pm.statuses[second] = processmanager.StatusStopped // simulated manual browser close
	if _, err := s.ListProfiles(); err != nil {
		t.Fatal("status refresh failed")
	}
	acceptanceWaitBridgeClosed(t, b)
	if len(s.bridges) != 0 {
		t.Fatal("orphan bridge after status refresh")
	}
}

func TestSafetyMissingProxyConfigFailsClosed(t *testing.T) {
	s, root, _ := acceptanceService(t)
	id := acceptanceProxy(t, s, "Missing network config")
	if err := os.Remove(filepath.Join(root, id, "network.json")); err != nil {
		t.Fatal("remove temporary config")
	}
	if err := s.LaunchProfile(id); err == nil {
		t.Fatal("missing network configuration allowed launch")
	}
	adapter := s.engine.(*fakeAdapter)
	if adapter.directCalls != 0 || adapter.proxyCalls != 0 {
		t.Fatal("missing configuration reached engine adapter")
	}
	views, err := s.ListProfiles()
	if err != nil || len(views) != 1 || views[0].LaunchReady || views[0].Network == "Direct" {
		t.Fatal("missing configuration must remain visible and launch-blocked")
	}
	if _, err := os.Stat(filepath.Join(root, id, "network.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("missing config was silently recreated")
	}
}

func TestSafetyStopReopenSerialized(t *testing.T) {
	s, _, pm := acceptanceService(t)
	id := acceptanceProxy(t, s, "Stop relaunch interleaving")
	if err := s.LaunchProfile(id); err != nil {
		t.Fatal("mock launch failed")
	}
	oldBridge := s.bridges[id]
	t.Cleanup(func() { _ = oldBridge.Close() })
	stopping := &acceptanceStopManager{fakeProcessManager: pm, stopped: make(chan struct{}), resume: make(chan struct{})}
	s.processes = stopping
	finished := make(chan error, 1)
	go func() { finished <- s.StopProfile(id) }()
	<-stopping.stopped
	// The old process has exited, while its StopProfile call is still returning.
	relaunched := make(chan error, 1)
	go func() { relaunched <- s.LaunchProfile(id) }()
	select {
	case <-relaunched:
		close(stopping.resume)
		<-finished
		t.Fatal("relaunch interleaved with old Stop")
	case <-time.After(30 * time.Millisecond):
	}
	close(stopping.resume)
	if err := <-finished; err != nil {
		t.Fatal("mock stop failed")
	}
	select {
	case err := <-relaunched:
		if err != nil {
			t.Fatal("relaunch failed")
		}
	case <-time.After(time.Second):
		t.Fatal("lifecycle deadlock")
	}
	newBridge := s.bridges[id]
	acceptanceWaitBridgeClosed(t, oldBridge)
	if newBridge == nil || newBridge == oldBridge || acceptanceBridgeClosed(newBridge) {
		t.Fatal("replacement bridge damaged by old Stop")
	}
	if pm.statuses[id] != processmanager.StatusRunning {
		t.Fatal("replacement browser must still be running")
	}
}

func TestSafetyManualCloseGenerationOwnership(t *testing.T) {
	s, _, pm := acceptanceService(t)
	id := acceptanceProxy(t, s, "Manual close generations")
	if err := s.LaunchProfile(id); err != nil {
		t.Fatal("launch failed")
	}
	old := s.currentBridge(id)
	pm.statuses[id] = processmanager.StatusStopped
	// Reopen before the UI observes the manual exit: old resources still retire.
	if err := s.LaunchProfile(id); err != nil {
		t.Fatal("reopen failed")
	}
	next := s.currentBridge(id)
	acceptanceWaitBridgeClosed(t, old)
	s.closeBridge(id, old) // delayed cleanup for the previous generation
	if s.currentBridge(id) != next || acceptanceBridgeClosed(next) {
		t.Fatal("stale cleanup closed replacement bridge")
	}
	if err := s.StopProfile(id); err != nil {
		t.Fatal("stop failed")
	}
	acceptanceWaitBridgeClosed(t, next)
	if s.currentBridge(id) != nil {
		t.Fatal("orphan bridge")
	}
}

type acceptanceFailedAdapter struct{ *fakeAdapter }

func (a acceptanceFailedAdapter) LaunchSpecWithNetwork(profile.Profile, brnetwork.EngineProxy) (engine.LaunchSpec, error) {
	return engine.LaunchSpec{}, errors.New("injected adapter failure")
}

type acceptanceFailedLauncher struct{ *fakeProcessManager }

func (p acceptanceFailedLauncher) Launch(string, engine.LaunchSpec) error {
	return errors.New("injected process launch failure")
}

func (p acceptanceFailedLauncher) LaunchPrepared(id string, prepare func() (engine.LaunchSpec, error)) error {
	if _, err := prepare(); err != nil {
		return err
	}
	return p.Launch(id, engine.LaunchSpec{})
}

func TestSafetyFailedLaunchCleansOnlyOwnResources(t *testing.T) {
	for _, failure := range []string{"adapter", "process"} {
		t.Run(failure, func(t *testing.T) {
			s, _, pm := acceptanceService(t)
			id := acceptanceProxy(t, s, "Failed launch")
			var created *brnetwork.LoopbackBridge
			s.bridgeFactory = func(proxy *brnetwork.ResolvedProxy) (*brnetwork.LoopbackBridge, error) {
				var err error
				created, err = brnetwork.StartLoopbackBridge(proxy)
				return created, err
			}
			if failure == "adapter" {
				s.engine = acceptanceFailedAdapter{&fakeAdapter{}}
			} else {
				s.processes = acceptanceFailedLauncher{pm}
			}
			if s.LaunchProfile(id) == nil {
				t.Fatal("injected failure not returned")
			}
			if created == nil {
				t.Fatal("bridge was not created")
			}
			acceptanceWaitBridgeClosed(t, created)
			if s.currentBridge(id) != nil {
				t.Fatal("failed launch retained bridge")
			}
		})
	}
}

func TestSafetyCorruptConfigBlocksReadinessWithoutOverwriting(t *testing.T) {
	s, root, _ := acceptanceService(t)
	id := acceptanceProxy(t, s, "Corrupt config")
	path := filepath.Join(root, id, "network.json")
	if err := os.WriteFile(path, []byte("invalid-json"), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	before := acceptanceNetworkFiles(t, root, id)
	if _, err := s.GetNetwork(id); err == nil {
		t.Fatal("corrupt configuration accepted")
	}
	views, err := s.ListProfiles()
	if err != nil || len(views) != 1 || views[0].LaunchReady || views[0].Network == "Direct" {
		t.Fatal("corrupt config readiness incorrect")
	}
	if s.LaunchProfile(id) == nil {
		t.Fatal("corrupt configuration launched")
	}
	if before != acceptanceNetworkFiles(t, root, id) {
		t.Fatal("corrupt configuration was overwritten")
	}
}

func TestSafetyNewProfileExplicitlyInitializesDirect(t *testing.T) {
	s, root, _ := acceptanceService(t)
	p, err := s.CreateProfile("Explicit Direct")
	if err != nil {
		t.Fatal("create failed")
	}
	data, err := os.ReadFile(filepath.Join(root, p.ID, "network.json"))
	if err != nil || len(data) == 0 {
		t.Fatal("new profile has no explicit network record")
	}
	v, err := s.GetNetwork(p.ID)
	if err != nil || v.Mode != "direct" || !p.LaunchReady {
		t.Fatal("explicit Direct not ready")
	}
}
