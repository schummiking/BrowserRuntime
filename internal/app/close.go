//go:build windows || darwin

package app

import (
	"errors"
)

// PrepareClose is fail-closed, including while an operation holds the lock.
// No browser is stopped here. After approval, new launches are refused.
func (s *Service) PrepareClose() error {
	if !s.networkMu.TryLock() {
		return errors.New("stop all profiles before closing Browser Runtime")
	}
	defer s.networkMu.Unlock()
	if s.processes.HasActiveProfiles() {
		return errors.New("stop all profiles before closing Browser Runtime")
	}
	profiles, err := s.profiles.List()
	if err != nil {
		return errors.New("cannot verify browser status; close refused")
	}
	ids := make([]string, 0, len(profiles))
	for _, p := range profiles {
		ids = append(ids, p.ID)
	}
	return s.processes.WithStoppedProfiles(ids, func() error {
		for _, p := range profiles {
			s.closeBridge(p.ID, s.currentBridge(p.ID))
		}
		s.closing = true
		return nil
	})
}
