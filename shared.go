package main

import (
	"flag"
	"log"
	"log/slog"
	"os"
	"path"

	"github.com/FPGSchiba/vcs-srs-server/app"
	"github.com/FPGSchiba/vcs-srs-server/events"
	"github.com/FPGSchiba/vcs-srs-server/logging"
	slogmulti "github.com/samber/slog-multi"
)

// parseFlags parses CLI flags and builds two loggers:
//   - appLogger: includes BusHandler so log records stream to the frontend Logs tab
//   - wailsLogger: file + console only; used for Wails internals to prevent an
//     infinite loop (Wails logs each event it emits, which would re-trigger BusHandler)
func parseFlags(isHeadless bool, bus *events.EventBus) (configFilepath, bannedFilePath, distributionMode string, autoStartServers bool, appLogger, wailsLogger *slog.Logger) {
	var logFolder string
	var fileLogEnabled bool
	flag.StringVar(&configFilepath, "config", "config.yaml", "Path to the configuration file")
	flag.StringVar(&bannedFilePath, "banned", "banned_clients.json", "Path to the banned clients file")
	flag.StringVar(&logFolder, "log-folder", "log", "Folder to store log files")
	flag.BoolVar(&autoStartServers, "autostart", false, "Automatically start servers on application startup")
	flag.BoolVar(&fileLogEnabled, "file-log", true, "Enable file logging")
	if isHeadless {
		flag.StringVar(&distributionMode, "mode", "standalone", "Distribution mode (standalone, control, voice)")
	}
	flag.Parse()

	if fileLogEnabled {
		if _, err := os.Stat(logFolder); os.IsNotExist(err) {
			err := os.Mkdir(logFolder, 0755)
			if err != nil {
				log.Fatalf("error creating log directory: %v", err)
			}
		}

		f, err := os.OpenFile(path.Join(logFolder, "vcs-server-log.jsonl"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0644)
		if err != nil {
			log.Fatalf("error opening log file: %v", err)
		}
		wailsLogger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			slog.NewJSONHandler(f, nil),
		))
		appLogger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			slog.NewJSONHandler(f, nil),
			logging.NewBusHandler(bus),
		))
	} else {
		wailsLogger = slog.New(slog.NewTextHandler(os.Stdout, nil))
		appLogger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			logging.NewBusHandler(bus),
		))
	}

	appLogger.Info("Auto-start servers", "autostart", autoStartServers)
	appLogger.Info("Using config file", "config", configFilepath)
	appLogger.Info("Using banned clients file", "bannedFile", bannedFilePath)
	appLogger.Info("Using log folder", "logFolder", logFolder)
	appLogger.Info("File logging enabled", "fileLogEnabled", fileLogEnabled)
	appLogger.Info("Version", "version", app.Version)
	if isHeadless {
		appLogger.Info("Distribution mode", "mode", distributionMode)
	} else {
		appLogger.Info("Running in GUI mode")
	}

	return
}
