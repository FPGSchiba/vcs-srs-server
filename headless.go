//go:build headless

package main

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
)

func main() {
	// In headless mode, we don't start the Wails application.
	configFilepath, bannedFilePath, _, logger := parseFlags()

	vcs := app.New()

	vcs.HeadlessStartup(logger, configFilepath, bannedFilePath)

	select {} // Block forever
}
