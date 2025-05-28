//go:build !windows

package app

func (a *App) setupWindow() {
	// No-op for non-Windows platforms
	// TODO: Setup transparency or other window properties for Linux or macOS if needed
}
