//go:build headless

package main

import (
	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/state"
	"os"
)

func main() {
	// In headless mode, we don't start the Wails application.
	vcs := app.New()
	configFilepath, bannedFilePath, distributionModeFlag, _, appLogger, _, isGlobal := parseFlags(true, vcs.GetEventBus())
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
		appLogger.Error("Invalid distribution mode specified. Must be one of: standalone, control, voice")
		return
	}
	if isGlobal && distributionMode != state.DistributionModeVoice {
		appLogger.Error("--global is only valid with --mode voice")
		return
	}

	defer func() { // Ensure we catch any panics and log them
		if err := recover(); err != nil { //catch
			appLogger.Error("Application panicked", "error", err)
			os.Exit(1)
		}
	}()

	vcs.HeadlessStartup(appLogger, configFilepath, bannedFilePath, distributionMode, isGlobal)

	select {} // Block forever
}
