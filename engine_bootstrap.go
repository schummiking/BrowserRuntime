//go:build windows || darwin

package main

import (
	"browserruntime/internal/engine"
	"browserruntime/internal/engine/cloak"
	"browserruntime/internal/enginebootstrap"
	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/profile"
)

// EngineBootstrap exposes only non-secret progress and explicit user actions.
type EngineBootstrap struct{ manager *enginebootstrap.Manager }

func (b *EngineBootstrap) GetStatus() enginebootstrap.Status {
	if b.manager == nil {
		return enginebootstrap.Status{Enabled: true, Ready: false, Phase: "FAILED"}
	}
	return b.manager.GetStatus()
}
func (b *EngineBootstrap) Download() {
	if b.manager != nil {
		b.manager.Download()
	}
}
func (b *EngineBootstrap) Cancel() {
	if b.manager != nil {
		b.manager.Cancel()
	}
}

type portableEngineAdapter struct {
	manager      *enginebootstrap.Manager
	profilesRoot string
}

func (a *portableEngineAdapter) LaunchSpec(p profile.Profile) (engine.LaunchSpec, error) {
	return a.LaunchSpecWithNetwork(p, brnetwork.EngineProxy{Mode: brnetwork.ModeDirect})
}
func (a *portableEngineAdapter) LaunchSpecWithNetwork(p profile.Profile, n brnetwork.EngineProxy) (engine.LaunchSpec, error) {
	if a.manager.Verify() != nil {
		return engine.LaunchSpec{}, enginebootstrap.ErrNotReady
	}
	adapter, err := cloak.NewAdapter(a.manager.Executable(), a.profilesRoot)
	if err != nil {
		return engine.LaunchSpec{}, enginebootstrap.ErrNotReady
	}
	return adapter.LaunchSpecWithNetwork(p, n)
}
