//go:build windows || darwin

package processmanager

import (
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"browserruntime/internal/engine"
	"browserruntime/internal/profile"
)

const validTestProfileID = "br_0123456789abcdef0123456789abcdef"

type fakeProcess struct {
	pid         int
	done        chan struct{}
	closeOnce   sync.Once
	openCalls   int
	closeCalls  int
	closeExits  bool
	operationMu sync.Mutex
}

func newFakeProcess(pid int, closeExits bool) *fakeProcess {
	return &fakeProcess{pid: pid, done: make(chan struct{}), closeExits: closeExits}
}

func (p *fakeProcess) PID() int { return p.pid }
func (p *fakeProcess) Wait() error {
	<-p.done
	return nil
}
func (p *fakeProcess) OpenWindow() error {
	p.operationMu.Lock()
	defer p.operationMu.Unlock()
	p.openCalls++
	return nil
}
func (p *fakeProcess) CloseWindow() error {
	p.operationMu.Lock()
	p.closeCalls++
	closeExits := p.closeExits
	p.operationMu.Unlock()
	if closeExits {
		p.exit()
	}
	return nil
}
func (p *fakeProcess) exit() { p.closeOnce.Do(func() { close(p.done) }) }

type fakeLauncher struct {
	mu        sync.Mutex
	processes []*fakeProcess
	starts    int
}

func (l *fakeLauncher) Start(engine.LaunchSpec) (managedProcess, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.starts >= len(l.processes) {
		return nil, errors.New("no fake process configured")
	}
	process := l.processes[l.starts]
	l.starts++
	return process, nil
}

type fakeLease struct {
	provider *fakeLeaseProvider
	id       string
	once     sync.Once
}

func (l *fakeLease) Release() {
	l.once.Do(func() {
		l.provider.mu.Lock()
		delete(l.provider.held, l.id)
		l.provider.mu.Unlock()
	})
}

type fakeLeaseProvider struct {
	mu   sync.Mutex
	held map[string]bool
}

func newFakeLeaseProvider() *fakeLeaseProvider {
	return &fakeLeaseProvider{held: map[string]bool{}}
}

func (p *fakeLeaseProvider) Acquire(id string) (profileLease, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.held[id] {
		return nil, ErrProfileAlreadyRunning
	}
	p.held[id] = true
	return &fakeLease{provider: p, id: id}, nil
}

func (p *fakeLeaseProvider) isHeld(id string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.held[id]
}

func validSpec() engine.LaunchSpec {
	return engine.LaunchSpec{
		ExecutablePath:  filepath.Join(os.TempDir(), "cloak"),
		Arguments:       []string{"--fingerprint=12345"},
		ProfilePath:     `C:\profiles\` + validTestProfileID + `\browser-data`,
		FingerprintSeed: "12345",
	}
}

func newTestProcessManager(launcher processLauncher, leases leaseProvider, timeout time.Duration) *ProcessManager {
	m := newProcessManager(launcher, leases, timeout)
	m.externalOccupancy = func(string) (Status, error) { return StatusStopped, nil }
	return m
}

func waitForStatus(t *testing.T, manager *ProcessManager, want Status) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		got, err := manager.Status(validTestProfileID)
		if err != nil {
			t.Fatalf("Status() error = %v", err)
		}
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	got, _ := manager.Status(validTestProfileID)
	t.Fatalf("Status() = %q, want %q", got, want)
}

func TestLaunchOpenDuplicateAndManualExitCleanup(t *testing.T) {
	process := newFakeProcess(1001, false)
	launcher := &fakeLauncher{processes: []*fakeProcess{process}}
	leases := newFakeLeaseProvider()
	manager := newTestProcessManager(launcher, leases, 100*time.Millisecond)

	if status, err := manager.Status(validTestProfileID); err != nil || status != StatusStopped {
		t.Fatalf("initial Status() = %q, %v; want stopped", status, err)
	}
	if err := manager.Launch(validTestProfileID, validSpec()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}
	if status, err := manager.Status(validTestProfileID); err != nil || status != StatusRunning {
		t.Fatalf("running Status() = %q, %v; want running", status, err)
	}
	if !leases.isHeld(validTestProfileID) {
		t.Fatal("Launch() did not hold profile lease")
	}
	if err := manager.Open(validTestProfileID); err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if process.openCalls != 1 {
		t.Fatalf("OpenWindow() calls = %d, want 1", process.openCalls)
	}
	if err := manager.Launch(validTestProfileID, validSpec()); !errors.Is(err, ErrProfileAlreadyRunning) {
		t.Fatalf("duplicate Launch() error = %v, want ErrProfileAlreadyRunning", err)
	}

	process.exit()
	waitForStatus(t, manager, StatusStopped)
	if leases.isHeld(validTestProfileID) {
		t.Fatal("manual process exit did not release profile lease")
	}
}

func TestStopGracefullyClosesAndCleansState(t *testing.T) {
	process := newFakeProcess(1002, true)
	launcher := &fakeLauncher{processes: []*fakeProcess{process}}
	leases := newFakeLeaseProvider()
	manager := newTestProcessManager(launcher, leases, time.Second)
	if err := manager.Launch(validTestProfileID, validSpec()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	if err := manager.Stop(validTestProfileID); err != nil {
		t.Fatalf("Stop() error = %v", err)
	}
	if process.closeCalls != 1 {
		t.Fatalf("CloseWindow() calls = %d, want 1", process.closeCalls)
	}
	if status, err := manager.Status(validTestProfileID); err != nil || status != StatusStopped {
		t.Fatalf("Status() after Stop = %q, %v; want stopped", status, err)
	}
	if leases.isHeld(validTestProfileID) {
		t.Fatal("Stop() did not release profile lease")
	}
}

func TestStopTimeoutDoesNotForceKill(t *testing.T) {
	process := newFakeProcess(1003, false)
	launcher := &fakeLauncher{processes: []*fakeProcess{process}}
	manager := newTestProcessManager(launcher, newFakeLeaseProvider(), 20*time.Millisecond)
	if err := manager.Launch(validTestProfileID, validSpec()); err != nil {
		t.Fatalf("Launch() error = %v", err)
	}

	if err := manager.Stop(validTestProfileID); !errors.Is(err, ErrGracefulStopTimeout) {
		t.Fatalf("Stop() error = %v, want ErrGracefulStopTimeout", err)
	}
	if status, _ := manager.Status(validTestProfileID); status != StatusStopping {
		t.Fatalf("Status() after timeout = %q, want stopping", status)
	}
	select {
	case <-process.done:
		t.Fatal("Stop timeout force-closed fake process")
	default:
	}
	process.exit()
	waitForStatus(t, manager, StatusStopped)
}

func TestInvalidProfileIDRejectedByEveryOperation(t *testing.T) {
	manager := newTestProcessManager(&fakeLauncher{}, newFakeLeaseProvider(), time.Second)
	invalid := "../profile"

	if err := manager.Launch(invalid, validSpec()); !errors.Is(err, profile.ErrInvalidProfileID) {
		t.Errorf("Launch(invalid) error = %v, want ErrInvalidProfileID", err)
	}
	if err := manager.Open(invalid); !errors.Is(err, profile.ErrInvalidProfileID) {
		t.Errorf("Open(invalid) error = %v, want ErrInvalidProfileID", err)
	}
	if err := manager.Stop(invalid); !errors.Is(err, profile.ErrInvalidProfileID) {
		t.Errorf("Stop(invalid) error = %v, want ErrInvalidProfileID", err)
	}
	if _, err := manager.Status(invalid); !errors.Is(err, profile.ErrInvalidProfileID) {
		t.Errorf("Status(invalid) error = %v, want ErrInvalidProfileID", err)
	}
}
