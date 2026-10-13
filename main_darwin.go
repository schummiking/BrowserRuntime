package main

import (
	"context"
	"embed"
	"os/exec"
	"path/filepath"
	_ "time/tzdata"

	"browserruntime/internal/enginebootstrap"
	"browserruntime/internal/portable"
	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/menu"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/runtime"
)

//go:embed all:frontend/dist
var assets embed.FS

func main() {
	layout, err := portable.Current()
	if err != nil || layout.Prepare() != nil {
		startupError("BrowserRuntime cannot write its Application Support folder. / 无法写入应用数据目录。")
		return
	}
	bootstrap := enginebootstrap.New(filepath.Join(layout.Engines, "cloakbrowser", enginebootstrap.Version))
	defer bootstrap.Shutdown()
	go bootstrap.Check()
	service, err := buildPortableService(layout, bootstrap)
	if err != nil {
		startupError("BrowserRuntime could not open its application data. / 无法打开应用数据。")
		return
	}
	language := &UILanguage{}
	appMenu := menu.NewMenu()
	appMenu.Append(menu.AppMenu())
	appMenu.Append(menu.EditMenu())
	err = wails.Run(&options.App{
		Title: "Browser Runtime", Width: 1080, Height: 720, MinWidth: 760, MinHeight: 520,
		AssetServer:      &assetserver.Options{Assets: assets},
		BackgroundColour: &options.RGBA{R: 246, G: 248, B: 250, A: 1},
		Menu:             appMenu,
		Bind:             []interface{}{service, language, &EngineBootstrap{bootstrap}},
		OnBeforeClose: closeGuard(service.PrepareClose, func(ctx context.Context) {
			title, message := language.exitWarning()
			_, _ = runtime.MessageDialog(ctx, runtime.MessageDialogOptions{Type: runtime.WarningDialog, Title: title, Message: message})
		}),
	})
	if err != nil {
		startupError("BrowserRuntime could not start its macOS window. / 无法启动 macOS 窗口。")
	}
}

func startupError(message string) {
	// Only fixed non-secret messages enter the native startup dialog.
	_ = exec.Command("/usr/bin/osascript", "-e", `on run argv
display alert "BrowserRuntime" message (item 1 of argv) as critical
end run`, message).Run()
}
