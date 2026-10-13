//go:build windows || darwin

package processmanager

import (
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"browserruntime/internal/engine"
	"browserruntime/internal/profile"
)

var (
	ErrProfileAlreadyRunning = errors.New("profile already has a running browser")
	ErrProfileNotRunning     = errors.New("profile browser is not running")
	ErrGracefulStopTimeout   = errors.New("browser did not exit before graceful stop timeout")
	ErrExternalBrowser       = errors.New("Browser is still running outside this manager. Close it manually before continuing.")
	ErrOccupancyUnknown      = errors.New("Unable to confirm the browser has stopped. Operation blocked.")
	ErrProfileInUse          = errors.New("Profile is in use by another manager. Close it before continuing.")
)

// Status is the lifecycle state of a profile browser process.
type Status string

const (
	StatusStopped         Status = "STOPPED"
	StatusStarting        Status = "STARTING"
	StatusRunning         Status = "RUNNING"
	StatusStopping        Status = "STOPPING"
	StatusExternalRunning Status = "EXTERNAL_RUNNING"
	StatusInUse           Status = "IN_USE"
	StatusUnknown         Status = "UNKNOWN"
)

const defaultGracefulStopTimeout = 10 * time.Second

type managedProcess interface {
	PID() int
	Wait() error
	OpenWindow() error
	CloseWindow() error
}

type processLauncher interface {
	Start(engine.LaunchSpec) (managedProcess, error)
}

type profileLease interface {
	Release()
}

type leaseProvider interface {
	Acquire(profileID string) (profileLease, error)
}

type processRecord struct {
	process managedProcess
	lease   profileLease
	status  Status
	done    chan struct{}
}

// ProcessManager owns native browser process state for the current runtime.
type ProcessManager struct {
	mu                  sync.Mutex
	records             map[string]*processRecord
	launcher            processLauncher
	leases              leaseProvider
	gracefulStopTimeout time.Duration
	externalOccupancy   func(string) (Status, error)
}

func newProcessManager(launcher processLauncher, leases leaseProvider, gracefulStopTimeout time.Duration) *ProcessManager {
	return &ProcessManager{
		records:             make(map[string]*processRecord),
		launcher:            launcher,
		leases:              leases,
		gracefulStopTimeout: gracefulStopTimeout,
	}
}

// Launch starts one direct native browser process for a profile.
func (m *ProcessManager) Launch(profileID string, spec engine.LaunchSpec) error {
	return m.LaunchPrepared(profileID, func() (engine.LaunchSpec, error) { return spec, nil })
}

// LaunchPrepared holds the cross-manager lease through fresh occupancy checks,
// bridge/spec preparation, CreateProcess and the entire browser lifetime.
func (m *ProcessManager) LaunchPrepared(profileID string, prepare func() (engine.LaunchSpec, error)) error {
	if err := profile.ValidateID(profileID); err != nil {
		return err
	}
	record, err := m.reserveStopped(profileID, StatusStarting)
	if err != nil {
		return err
	}
	started := false
	defer func() {
		if !started {
			record.lease.Release()
			m.removeReservation(profileID, record)
		}
	}()
	spec, err := prepare()
	if err != nil {
		return err
	}
	if err := engine.ValidateLaunchSpec(spec); err != nil {
		return err
	}
	process, err := m.launcher.Start(spec)
	if err != nil {
		return fmt.Errorf("start native browser process: %w", err)
	}

	m.mu.Lock()
	record.process = process
	record.status = StatusRunning
	m.mu.Unlock()
	started = true
	go m.monitorProcess(profileID, record)
	return nil
}

// Open brings the existing browser window to the foreground without relaunching.
func (m *ProcessManager) Open(profileID string) error {
	if err := profile.ValidateID(profileID); err != nil {
		return err
	}

	m.mu.Lock()
	record, exists := m.records[profileID]
	if !exists || record.status != StatusRunning || record.process == nil {
		m.mu.Unlock()
		return ErrProfileNotRunning
	}
	process := record.process
	m.mu.Unlock()

	if err := process.OpenWindow(); err != nil {
		return fmt.Errorf("open browser window: %w", err)
	}
	return nil
}

// Stop asks the existing browser window to close and waits for graceful exit.
func (m *ProcessManager) Stop(profileID string) error {
	if err := profile.ValidateID(profileID); err != nil {
		return err
	}

	m.mu.Lock()
	record, exists := m.records[profileID]
	if !exists || record.status != StatusRunning || record.process == nil {
		m.mu.Unlock()
		return ErrProfileNotRunning
	}
	record.status = StatusStopping
	process := record.process
	done := record.done
	m.mu.Unlock()

	if err := process.CloseWindow(); err != nil {
		m.mu.Lock()
		if current, ok := m.records[profileID]; ok && current == record && current.status == StatusStopping {
			current.status = StatusRunning
		}
		m.mu.Unlock()
		return fmt.Errorf("request graceful browser close: %w", err)
	}

	timer := time.NewTimer(m.gracefulStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
		return nil
	case <-timer.C:
		return ErrGracefulStopTimeout
	}
}

// Status reports STOPPED only after both OS lease and browser checks succeed.
func (m *ProcessManager) Status(profileID string) (Status, error) {
	if err := profile.ValidateID(profileID); err != nil {
		return "", err
	}

	m.mu.Lock()
	record, exists := m.records[profileID]
	if exists {
		status := record.status
		m.mu.Unlock()
		return status, nil
	}
	m.mu.Unlock()
	lease, err := m.leases.Acquire(profileID)
	if errors.Is(err, ErrProfileAlreadyRunning) {
		return StatusInUse, nil
	}
	if err != nil {
		return StatusUnknown, nil
	}
	defer lease.Release()
	return m.checkedExternal(profileID), nil
}

func (m *ProcessManager) checkedExternal(id string) Status {
	if m.externalOccupancy == nil {
		return StatusUnknown
	}
	status, err := m.externalOccupancy(id)
	if err != nil {
		return StatusUnknown
	}
	if status == StatusStopped || status == StatusExternalRunning {
		return status
	}
	return StatusUnknown
}

func OccupancyError(status Status) error {
	switch status {
	case StatusExternalRunning:
		return ErrExternalBrowser
	case StatusUnknown:
		return ErrOccupancyUnknown
	case StatusInUse:
		return ErrProfileInUse
	default:
		return ErrProfileAlreadyRunning
	}
}

func (m *ProcessManager) reserveStopped(id string, reservedStatus Status) (*processRecord, error) {
	if err := profile.ValidateID(id); err != nil {
		return nil, err
	}
	record := &processRecord{status: reservedStatus, done: make(chan struct{})}
	m.mu.Lock()
	if previous, exists := m.records[id]; exists {
		status := previous.status
		m.mu.Unlock()
		return nil, OccupancyError(status)
	}
	m.records[id] = record
	m.mu.Unlock()
	lease, err := m.leases.Acquire(id)
	if err != nil {
		m.removeReservation(id, record)
		if errors.Is(err, ErrProfileAlreadyRunning) {
			return nil, ErrProfileInUse
		}
		return nil, ErrOccupancyUnknown
	}
	if status := m.checkedExternal(id); status != StatusStopped {
		lease.Release()
		m.removeReservation(id, record)
		return nil, OccupancyError(status)
	}
	record.lease = lease
	return record, nil
}

// WithStoppedProfiles keeps every lease until the protected action finishes.
// Acquisitions are nonblocking and ordered; no callback may reenter this guard.
func (m *ProcessManager) WithStoppedProfiles(ids []string, action func() error) error {
	ordered := append([]string(nil), ids...)
	sort.Strings(ordered)
	reserved := make(map[string]*processRecord)
	defer func() {
		for id, record := range reserved {
			record.lease.Release()
			m.removeReservation(id, record)
		}
	}()
	for _, id := range ordered {
		if _, exists := reserved[id]; exists {
			continue
		}
		record, err := m.reserveStopped(id, StatusInUse)
		if err != nil {
			return err
		}
		reserved[id] = record
	}
	return action()
}

// HasActiveProfiles includes tracked processes even if profile metadata is lost.
func (m *ProcessManager) HasActiveProfiles() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.records) != 0
}

func (m *ProcessManager) monitorProcess(profileID string, record *processRecord) {
	_ = record.process.Wait()
	record.lease.Release()

	m.mu.Lock()
	if current, exists := m.records[profileID]; exists && current == record {
		delete(m.records, profileID)
	}
	close(record.done)
	m.mu.Unlock()
}

func (m *ProcessManager) removeReservation(profileID string, record *processRecord) {
	m.mu.Lock()
	if current, exists := m.records[profileID]; exists && current == record {
		delete(m.records, profileID)
	}
	m.mu.Unlock()
}
