//go:build headless

package main

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"os"
)

func main() {
	// In headless mode, we don't start the Wails application.
	configFilepath, bannedFilePath, distributionModeFlag, _, logger := parseFlags(true)
	distributionMode := state.DistributionModeStandalone
	switch distributionModeFlag {
	case "standalone":
		distributionMode = state.DistributionModeStandalone
		break
	case "control":
		distributionMode = state.DistributionModeControl
		break
	case "voice":
		distributionMode = state.DistributionModeVoice
		break
	default:
		logger.Error("Invalid distribution mode specified. Must be one of: standalone, control, voice")
		return
	}

	vcs := app.New()

	defer func() { // Ensure we catch any panics and log them
		if err := recover(); err != nil { //catch
			logger.Error("Application panicked", "error", err)
			os.Exit(1)
		}
	}()

	vcs.HeadlessStartup(logger, configFilepath, bannedFilePath, distributionMode)

	select {} // Block forever
}
