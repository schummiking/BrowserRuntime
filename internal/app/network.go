//go:build windows || darwin

package app

import (
	"context"
	"errors"
	"time"

	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/processmanager"
)

const (
	networkDirect    = "Direct"
	networkNeedsTest = "Needs Test"
	networkReady     = "Ready"
	networkFailed    = "Connection Failed"
)

// Output DTOs deliberately contain no secret or core implementation objects.
type LocalityView struct {
	ExitIP      string `json:"exit_ip"`
	CountryCode string `json:"country_code"`
	City        string `json:"city,omitempty"`
	Timezone    string `json:"timezone"`
	Locale      string `json:"locale"`
}

type NetworkView struct {
	Mode        string        `json:"mode"`
	Scheme      string        `json:"scheme,omitempty"`
	Host        string        `json:"host,omitempty"`
	Port        uint16        `json:"port,omitempty"`
	Username    string        `json:"username,omitempty"`
	HasPassword bool          `json:"has_password"`
	Status      string        `json:"status"`
	Locality    *LocalityView `json:"locality,omitempty"`
}

// Password is a write-only optional replacement, never a stored-secret response.
type SetProxyInput struct {
	Scheme   string `json:"scheme"`
	Host     string `json:"host"`
	Port     uint16 `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

// Formatting is for diagnostics only; JSON binding retains the normal editable
// username and write-only replacement password contract.
func (v NetworkView) String() string {
	return (brnetwork.Config{Mode: brnetwork.Mode(v.Mode), Scheme: brnetwork.Scheme(v.Scheme), Host: v.Host, Port: v.Port}).String()
}

func (v NetworkView) GoString() string { return v.String() }

func (v SetProxyInput) String() string {
	return (brnetwork.ResolvedProxy{Scheme: brnetwork.Scheme(v.Scheme), Host: v.Host, Port: v.Port}).Redacted()
}

func (v SetProxyInput) GoString() string { return v.String() }

func (s *Service) GetNetwork(id string) (*NetworkView, error) {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	return s.networkView(id)
}

func (s *Service) SetDirect(id string) (*NetworkView, error) {
	return s.withEditableNetwork(id, func() (*NetworkView, error) {
		if err := s.networks.SetDirect(id); err != nil {
			return nil, safeNetworkError(err)
		}
		delete(s.networkFailures, id)
		return s.networkView(id)
	})
}

func (s *Service) SetProxy(id string, input SetProxyInput) (*NetworkView, error) {
	return s.withEditableNetwork(id, func() (*NetworkView, error) {
		config := brnetwork.Config{Mode: brnetwork.ModeProxy, Scheme: brnetwork.Scheme(input.Scheme), Host: input.Host, Port: input.Port, Username: input.Username}
		if err := config.Validate(); err != nil {
			return nil, safeNetworkError(err)
		}
		password := input.Password
		if password == "" && config.Username != "" {
			current, err := s.networks.Get(id)
			if err != nil {
				return nil, safeNetworkError(err)
			}
			if current.Mode == brnetwork.ModeProxy && current.Username != "" {
				resolved, err := s.networks.ResolveProxy(id)
				if err != nil {
					return nil, brnetwork.ErrSecretUnavailable
				}
				password = resolved.Password
				resolved.Password = ""
			}
		}
		if err := s.networks.SetProxy(id, config, password); err != nil {
			return nil, safeNetworkError(err)
		}
		delete(s.networkFailures, id)
		return s.networkView(id)
	})
}

func (s *Service) TestNetwork(id string) (*NetworkView, error) {
	return s.withEditableNetwork(id, func() (*NetworkView, error) { return s.testNetwork(id) })
}

func (s *Service) withEditableNetwork(id string, action func() (*NetworkView, error)) (*NetworkView, error) {
	s.networkMu.Lock()
	defer s.networkMu.Unlock()
	if err := s.requireNetworkEditable(id); err != nil {
		return nil, err
	}
	var view *NetworkView
	err := s.processes.WithStoppedProfiles([]string{id}, func() error {
		var err error
		view, err = action()
		return err
	})
	return view, err
}

func (s *Service) testNetwork(id string) (*NetworkView, error) {
	config, err := s.networks.Get(id)
	if err != nil {
		return nil, safeNetworkError(err)
	}
	if config.Mode == brnetwork.ModeDirect {
		return s.networkView(id)
	}
	// Invalidate persisted locality before testing. A failed test must remain
	// launch-blocked even after this application is restarted.
	s.networkFailures[id] = true
	resolved, err := s.networks.ResolveProxy(id)
	if err != nil {
		return s.networkView(id)
	}
	if err := s.networks.SetProxy(id, *config, resolved.Password); err != nil {
		return nil, safeNetworkError(err)
	}
	resolved.Password = ""
	ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
	defer cancel()
	result, err := s.networks.TestConnection(ctx, id, s.tester)
	if err != nil || !result.OK {
		return s.networkView(id)
	}
	if _, err := s.networks.ResolveLocality(ctx, id, result.ExitIP, s.resolver); err != nil {
		return s.networkView(id)
	}
	delete(s.networkFailures, id)
	return s.networkView(id)
}

func (s *Service) networkView(id string) (*NetworkView, error) {
	config, err := s.networks.Get(id)
	if err != nil {
		return nil, safeNetworkError(err)
	}
	view := &NetworkView{Mode: string(config.Mode), Scheme: string(config.Scheme), Host: config.Host, Port: config.Port, Username: config.Username, Status: networkDirect}
	if config.Mode == brnetwork.ModeDirect {
		return view, nil
	}
	view.Status = networkNeedsTest
	secretOK := true
	if config.Username != "" {
		resolved, err := s.networks.ResolveProxy(id)
		secretOK = err == nil && resolved != nil && resolved.Password != ""
		view.HasPassword = secretOK
		if resolved != nil {
			resolved.Password = ""
		}
	}
	if s.networkFailures[id] {
		view.Status = networkFailed
		return view, nil
	}
	if config.Locality != nil && config.Locality.Validate() == nil && secretOK {
		locality := config.Locality
		view.Status = networkReady
		view.Locality = &LocalityView{ExitIP: locality.ExitIP, CountryCode: locality.CountryCode, City: locality.City, Timezone: locality.Timezone, Locale: locality.Locale}
	}
	return view, nil
}

func (s *Service) requireNetworkEditable(id string) error {
	if _, err := s.profiles.Get(id); err != nil {
		return err
	}
	status, err := s.processes.Status(id)
	if err != nil {
		return err
	}
	if status != processmanager.StatusStopped {
		if status == processmanager.StatusExternalRunning || status == processmanager.StatusUnknown || status == processmanager.StatusInUse {
			return processmanager.OccupancyError(status)
		}
		return errors.New("stop browser before changing or testing network")
	}
	return nil
}

func networkLabel(view *NetworkView) string {
	if view.Status != networkReady {
		return view.Status
	}
	if view.Locality.City == "" {
		return view.Locality.CountryCode
	}
	return view.Locality.CountryCode + " · " + view.Locality.City
}

func safeNetworkError(err error) error {
	for _, known := range []error{brnetwork.ErrSecretRequired, brnetwork.ErrSecretUnavailable, brnetwork.ErrConfigMissing, brnetwork.ErrInvalidConfig, brnetwork.ErrInvalidLocality, brnetwork.ErrLocalityUnavailable, brnetwork.ErrConnectionFailed} {
		if errors.Is(err, known) {
			return known
		}
	}
	return errors.New("network operation could not be completed")
}
