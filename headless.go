//go:build headless

package main

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
)

func main() {
	// In headless mode, we don't start the Wails application.
	configFilepath, bannedFilePath, distributionModeFlag, _, logger := parseFlags(true)
	distributionMode := app.DistributionModeStandalone
	switch distributionModeFlag {
	case "standalone":
		distributionMode = app.DistributionModeStandalone
		break
	case "control":
		distributionMode = app.DistributionModeControl
		break
	case "voice":
		distributionMode = app.DistributionModeVoice
		break
	default:
		logger.Error("Invalid distribution mode specified. Must be one of: standalone, control, voice")
		return
	}

	vcs := app.New()

	vcs.HeadlessStartup(logger, configFilepath, bannedFilePath, distributionMode)

	select {} // Block forever
}
