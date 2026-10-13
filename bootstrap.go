//go:build windows || darwin

package main

import (
	appservice "browserruntime/internal/app"
	"browserruntime/internal/enginebootstrap"
	brnetwork "browserruntime/internal/network"
	"browserruntime/internal/portable"
	"browserruntime/internal/processmanager"
	"browserruntime/internal/profile"
)

func buildPortableService(l portable.Layout, bootstrap *enginebootstrap.Manager) (*appservice.Service, error) {
	profiles, err := profile.NewStore(l.Profiles)
	if err != nil {
		return nil, err
	}
	networks, err := brnetwork.NewStore(l.Profiles)
	if err != nil {
		return nil, err
	}
	tester, err := brnetwork.NewTester("https://api.ipify.org?format=json")
	if err != nil {
		return nil, err
	}
	resolver, err := brnetwork.NewHTTPJSONLocalityResolver("https://ipwho.is")
	if err != nil {
		return nil, err
	}
	manager, err := processmanager.NewProcessManagerWithPaths(l.Profiles, bootstrap.Executable())
	if err != nil {
		return nil, err
	}
	return appservice.NewService(profiles, networks, &portableEngineAdapter{bootstrap, l.Profiles}, manager, brnetwork.StartLoopbackBridge, tester, resolver)
}
