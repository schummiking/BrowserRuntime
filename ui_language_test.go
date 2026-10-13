//go:build windows || darwin

package main

import "testing"

func TestNativeExitWarningLanguage(t *testing.T) {
	ui := &UILanguage{}
	for _, language := range []string{"en-US", "zh-CN", "en-US"} {
		if err := ui.SetLanguage(language); err != nil {
			t.Fatal("language rejected")
		}
		title, message := ui.exitWarning()
		if language == "zh-CN" {
			if title != "Browser Runtime 仍在使用中" || message != "已拒绝关闭。请先停止所有运行中的浏览器环境。如果无法确认状态，请先解决状态错误再退出。" {
				t.Fatal("Chinese exit warning missing")
			}
		} else if title != "Browser Runtime is still in use" || message != "Close refused. Please Stop all running profiles first. If status cannot be verified, resolve that error before exiting." {
			t.Fatal("English exit warning changed")
		}
	}
	if ui.SetLanguage("unsupported") == nil {
		t.Fatal("unknown language accepted")
	}
	defaultTitle, _ := (&UILanguage{}).exitWarning()
	if defaultTitle != "Browser Runtime is still in use" {
		t.Fatal("native default is not English")
	}
}
