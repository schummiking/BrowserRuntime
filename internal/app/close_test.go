//go:build windows || darwin

package app

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"browserruntime/internal/processmanager"
)

type closeStatusError struct{ *fakeProcessManager }

func (p closeStatusError) Status(string) (processmanager.Status, error) {
	return "", errors.New("status unavailable")
}

func (p closeStatusError) WithStoppedProfiles([]string, func() error) error {
	return errors.New("status unavailable")
}

func TestSafetyUnreadableProfileListBlocksClose(t *testing.T) {
	s, root, _ := acceptanceService(t)
	p, err := s.CreateProfile("Unreadable status list")
	if err != nil {
		t.Fatal("setup failed")
	}
	if err := os.WriteFile(filepath.Join(root, p.ID, "profile.json"), []byte("invalid-json"), 0600); err != nil {
		t.Fatal("fixture write failed")
	}
	if s.PrepareClose() == nil {
		t.Fatal("unreliable profile list allowed exit")
	}
}

func TestSafetyTrackedProfileAbsentFromDiskBlocksClose(t *testing.T) {
	s, root, pm := acceptanceService(t)
	p, err := s.CreateProfile("Tracked but missing")
	if err != nil {
		t.Fatal("setup failed")
	}
	pm.statuses[p.ID] = processmanager.StatusRunning
	if err := os.RemoveAll(filepath.Join(root, p.ID)); err != nil {
		t.Fatal("remove isolated fixture")
	}
	if s.PrepareClose() == nil {
		t.Fatal("active tracked process omitted from disk list allowed exit")
	}
}

func TestSafetyCloseGate(t *testing.T) {
	for _, status := range []processmanager.Status{processmanager.StatusStarting, processmanager.StatusRunning, processmanager.StatusStopping, "UNKNOWN", processmanager.StatusStopped} {
		t.Run(string(status), func(t *testing.T) {
			s, _, pm := acceptanceService(t)
			p, err := s.CreateProfile("Close gate")
			if err != nil {
				t.Fatal("setup failed")
			}
			pm.statuses[p.ID] = status
			err = s.PrepareClose()
			if (err == nil) != (status == processmanager.StatusStopped) {
				t.Fatal("incorrect exit decision")
			}
			if status == processmanager.StatusStopped && s.LaunchProfile(p.ID) == nil {
				t.Fatal("launch raced with approved exit")
			}
		})
	}
	s, _, pm := acceptanceService(t)
	if _, err := s.CreateProfile("Status error"); err != nil {
		t.Fatal("setup failed")
	}
	s.processes = closeStatusError{pm}
	if s.PrepareClose() == nil {
		t.Fatal("status error allowed exit")
	}
	s.processes = pm
	s.networkMu.Lock()
	if s.PrepareClose() == nil {
		t.Fatal("in-flight operation allowed exit")
	}
	s.networkMu.Unlock()
}
