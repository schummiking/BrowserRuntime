//go:build windows

package processmanager

import "path/filepath"

// NewProcessManager creates the Windows native BrowserRuntime process manager.
func NewProcessManager() *ProcessManager {
	return newProcessManager(windowsNativeLauncher{}, windowsMutexLeaseProvider{}, defaultGracefulStopTimeout)
}

// NewProcessManagerWithPaths enables fresh Windows occupancy checks for this
// profiles root and native engine. An unconfigured manager fails closed.
func NewProcessManagerWithPaths(root, executable string) (*ProcessManager, error) {
	root, err := canonicalWindowsPath(root)
	if err != nil {
		return nil, ErrOccupancyUnknown
	}
	executable, err = canonicalWindowsPath(executable)
	if err != nil {
		return nil, ErrOccupancyUnknown
	}
	m := NewProcessManager()
	m.externalOccupancy = func(id string) (Status, error) {
		observed, err := scanWindowsBrowsers(executable)
		if err != nil {
			return StatusUnknown, ErrOccupancyUnknown
		}
		return classifyBrowserOccupancy(filepath.Join(root, id, "browser-data"), observed), nil
	}
	return m, nil
}
