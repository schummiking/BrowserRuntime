//go:build windows || darwin

package app

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"browserruntime/internal/engine"
	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/processmanager"
	"browserruntime/internal/profile"
)

var ErrRunningProfileDelete = errors.New("running profile cannot be deleted")

type ProfileView struct {
	ID                  string `json:"id"`
	Name                string `json:"name"`
	Browser             string `json:"browser"`
	Network             string `json:"network"`
	Status              string `json:"status"`
	LaunchReady         bool   `json:"launch_ready"`
	LaunchBlockedReason string `json:"launch_blocked_reason,omitempty"`
}

type profileStore interface {
	Create(string) (*profile.Profile, error)
	List() ([]profile.Profile, error)
	Get(string) (*profile.Profile, error)
	Rename(string, string) (*profile.Profile, error)
	Delete(string) error
}

type networkStore interface {
	SetDirect(string) error
	SetProxy(string, brnetwork.Config, string) error
	Get(string) (*brnetwork.Config, error)
	ResolveProxy(string) (*brnetwork.ResolvedProxy, error)
	TestConnection(context.Context, string, brnetwork.ConnectionTester) (brnetwork.ConnectionResult, error)
	ResolveLocality(context.Context, string, string, brnetwork.LocalityResolver) (*brnetwork.Locality, error)
}

type engineAdapter interface {
	LaunchSpec(profile.Profile) (engine.LaunchSpec, error)
	LaunchSpecWithNetwork(profile.Profile, brnetwork.EngineProxy) (engine.LaunchSpec, error)
}

type processManager interface {
	Launch(string, engine.LaunchSpec) error
	LaunchPrepared(string, func() (engine.LaunchSpec, error)) error
	WithStoppedProfiles([]string, func() error) error
	Open(string) error
	Stop(string) error
	Status(string) (processmanager.Status, error)
	HasActiveProfiles() bool
}

type Service struct {
	profiles      profileStore
	networks      networkStore
	engine        engineAdapter
	processes     processManager
	bridgeFactory func(*brnetwork.ResolvedProxy) (*brnetwork.LoopbackBridge, error)

	mu      sync.Mutex
	bridges map[string]*brnetwork.LoopbackBridge
	// Serialize UI network changes, tests and launch/delete readiness decisions.
	networkMu       sync.Mutex
	closing         bool // guarded by networkMu; prevents launch after close approval
	networkFailures map[string]bool
	tester          brnetwork.ConnectionTester
	resolver        brnetwork.LocalityResolver
}

func NewService(
	profiles profileStore,
	networks networkStore,
	engine engineAdapter,
	processes processManager,
	bridgeFactory func(*brnetwork.ResolvedProxy) (*brnetwork.LoopbackBridge, error),
	tester brnetwork.ConnectionTester,
	resolver brnetwork.LocalityResolver,
) (*Service, error) {
	if profiles == nil || networks == nil || engine == nil || processes == nil || bridgeFactory == nil || tester == nil || resolver == nil {
		return nil, errors.New("BrowserRuntime application dependencies are required")
	}
	return &Service{
		profiles:        profiles,
		networks:        networks,
		engine:          engine,
		processes:       processes,
		bridgeFactory:   bridgeFactory,
		bridges:         make(map[string]*brnetwork.LoopbackBridge),
		networkFailures: make(map[string]bool),
		tester:          tester,
		resolver:        resolver,
	}, nil
}

func (s *Service) ListProfiles() ([]ProfileView, error) {
	profiles, err := s.profiles.List()
	if err != nil {
		return nil, err
	}
	views := make([]ProfileView, 0, len(profiles))
	for _, item := range profiles {
		view, err := s.profileView(item)
		if err != nil {
			return nil, err
		}
		views = append(views, view)
	}
	return views, nil
}

func (s *Service) CreateProfile(name string) (*ProfileView, error) {
	created, err := s.profiles.Create(name)
	if err != nil {
		return nil, err
	}
	if err := s.networks.SetDirect(created.ID); err != nil {
		_ = s.profiles.Delete(created.ID)
		return nil, fmt.Errorf("initialize profile network: %w", err)
	}
	view, err := s.profileView(*created)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *Service) RenameProfile(id string, newName string) (*ProfileView, error) {
	renamed, err := s.profiles.Rename(id, newName)
	if err != nil {
		return nil, err
	}
	view, err := s.profileView(*renamed)
	if err != nil {
		return nil, err
	}
	return &view, nil
}

func (s *Service) DeleteProfile(id string) error {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	status, err := s.processes.Status(id)
	if err != nil {
		return err
	}
	if status != processmanager.StatusStopped {
		if status == processmanager.StatusExternalRunning || status == processmanager.StatusUnknown || status == processmanager.StatusInUse {
			return processmanager.OccupancyError(status)
		}
		return ErrRunningProfileDelete
	}
	return s.processes.WithStoppedProfiles([]string{id}, func() error {
		s.closeBridge(id, s.currentBridge(id))
		if err := s.profiles.Delete(id); err != nil {
			return err
		}
		delete(s.networkFailures, id)
		return nil
	})
}

func (s *Service) LaunchProfile(id string) error {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	if s.closing {
		return errors.New("application is closing")
	}
	var bridge *brnetwork.LoopbackBridge
	err := s.processes.LaunchPrepared(id, func() (engine.LaunchSpec, error) {
		// The OS profile lease is held before reading readiness or starting a bridge.
		s.closeBridge(id, s.currentBridge(id))
		p, err := s.profiles.Get(id)
		if err != nil {
			return engine.LaunchSpec{}, err
		}
		config, err := s.networks.Get(id)
		if err != nil {
			return engine.LaunchSpec{}, safeNetworkError(err)
		}
		view, err := s.networkView(id)
		if err != nil {
			return engine.LaunchSpec{}, err
		}
		if view.Status != networkDirect && view.Status != networkReady {
			return engine.LaunchSpec{}, brnetwork.ErrLocalityUnavailable
		}

		var spec engine.LaunchSpec
		if config.Mode == brnetwork.ModeDirect {
			spec, err = s.engine.LaunchSpec(*p)
		} else {
			if config.Locality == nil {
				return engine.LaunchSpec{}, brnetwork.ErrLocalityUnavailable
			}
			resolved, resolveErr := s.networks.ResolveProxy(id)
			if resolveErr != nil {
				return engine.LaunchSpec{}, resolveErr
			}
			bridge, err = s.bridgeFactory(resolved)
			if err != nil {
				return engine.LaunchSpec{}, err
			}
			engineProxy := bridge.EngineProxy()
			locality := *config.Locality
			engineProxy.Locality = &locality
			spec, err = s.engine.LaunchSpecWithNetwork(*p, engineProxy)
		}
		return spec, err
	})
	if err != nil {
		if bridge != nil {
			_ = bridge.Close()
		}
		return err
	}
	if bridge != nil {
		s.mu.Lock()
		s.bridges[id] = bridge
		s.mu.Unlock()
	}
	return nil
}

func (s *Service) OpenProfile(id string) error {
	return s.processes.Open(id)
}

func (s *Service) StopProfile(id string) error {
	// Reuse the existing operation lock: PM's exit monitor never calls Service,
	// so waiting for graceful exit cannot deadlock on this lock.
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	bridge := s.currentBridge(id)
	if err := s.processes.Stop(id); err != nil {
		return err
	}
	s.closeBridge(id, bridge)
	return nil
}

func (s *Service) profileView(item profile.Profile) (ProfileView, error) {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	network, err := s.networkView(item.ID)
	if err != nil {
		if !errors.Is(err, brnetwork.ErrConfigMissing) && !errors.Is(err, brnetwork.ErrInvalidConfig) {
			return ProfileView{}, err
		}
		// Keep the row visible, but never present lost/corrupt configuration as
		// Direct or launch-ready. GetNetwork still reports the explicit error.
		network = &NetworkView{Status: "Configuration Error"}
	}
	status, err := s.processes.Status(item.ID)
	if err != nil {
		return ProfileView{}, err
	}
	if status == processmanager.StatusStopped {
		s.closeBridge(item.ID, s.currentBridge(item.ID))
	}
	ready := network.Status == networkDirect || network.Status == networkReady
	blockedReason := ""
	if !ready {
		blockedReason = "Test network before launch"
		if network.Status == "Configuration Error" {
			blockedReason = "Network configuration unavailable; explicitly configure network"
		}
	}
	if status != processmanager.StatusStopped && status != processmanager.StatusRunning {
		ready = false
		blockedReason = processmanager.OccupancyError(status).Error()
	}
	return ProfileView{
		ID:                  item.ID,
		Name:                item.Name,
		Browser:             "CloakBrowser",
		Network:             networkLabel(network),
		Status:              statusLabel(status),
		LaunchReady:         ready,
		LaunchBlockedReason: blockedReason,
	}, nil
}

func statusLabel(status processmanager.Status) string {
	switch status {
	case processmanager.StatusStopped:
		return "Stopped"
	case processmanager.StatusRunning:
		return "Running"
	case processmanager.StatusStarting:
		return "Starting"
	case processmanager.StatusStopping:
		return "Stopping"
	case processmanager.StatusExternalRunning:
		return "ExternalRunning"
	case processmanager.StatusInUse:
		return "InUse"
	default:
		return "Unknown"
	}
}

func (s *Service) currentBridge(id string) *brnetwork.LoopbackBridge {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.bridges[id]
}

// A delayed cleanup may retire only the bridge generation it observed.
func (s *Service) closeBridge(id string, owner *brnetwork.LoopbackBridge) {
	s.mu.Lock()
	if owner != nil && s.bridges[id] == owner {
		delete(s.bridges, id)
	}
	s.mu.Unlock()
	if owner != nil {
		_ = owner.Close()
	}
}
