//go:build headless

package app

// guiApp is an empty placeholder in headless builds so that VCSApplication can
// embed it unconditionally without pulling in the Wails dependency.
// Wails requires CGO on Linux and cannot be compiled with CGO_ENABLED=0.
type guiApp struct{}
