//go:build windows || darwin

package main

import (
	_ "embed"
	"encoding/json"
	"errors"
	"sync"
)

// Native presentation shares the frontend's static dictionaries. No backend
// language preference is persisted and no Profile/Network data is consulted.
//
//go:embed frontend/src/i18n/en.json
var englishUI []byte

//go:embed frontend/src/i18n/zh-CN.json
var chineseUI []byte

type UILanguage struct {
	mu       sync.RWMutex
	language string
}

func (u *UILanguage) SetLanguage(language string) error {
	if language != "en-US" && language != "zh-CN" {
		return errors.New("unsupported UI language")
	}
	u.mu.Lock()
	u.language = language
	u.mu.Unlock()
	return nil
}

func (u *UILanguage) exitWarning() (string, string) {
	u.mu.RLock()
	language := u.language
	u.mu.RUnlock()
	data := englishUI
	if language == "zh-CN" {
		data = chineseUI
	}
	var dictionary map[string]string
	_ = json.Unmarshal(data, &dictionary)
	return dictionary["Browser Runtime is still in use"], dictionary["Close refused. Please Stop all running profiles first. If status cannot be verified, resolve that error before exiting."]
}
