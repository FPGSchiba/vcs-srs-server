//go:build windows

package app

import (
	"github.com/lxn/win"
	"syscall"
)

func (a *App) setupWindow() {
	hwnd := win.FindWindow(nil, syscall.StringToUTF16Ptr("vcs-server"))
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, win.GetWindowLong(hwnd, win.GWL_EXSTYLE)|win.WS_EX_LAYERED)
}
