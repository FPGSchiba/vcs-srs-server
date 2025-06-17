package main

import (
	"flag"
	"github.com/FPGSchiba/vcs-srs-server/app"
	slogmulti "github.com/samber/slog-multi"
	"log"
	"log/slog"
	"os"
	"path"
)

func parseFlags(isHeadless bool) (configFilepath, bannedFilePath, distributionMode string, autoStartServers bool, logger *slog.Logger) {
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
			err := os.Mkdir(logFolder, 0777)
			if err != nil {
				log.Fatalf("error creating log directory: %v", err)
			}
		}

		f, err := os.OpenFile(path.Join(logFolder, "vcs-server-log.jsonl"), os.O_RDWR|os.O_CREATE|os.O_APPEND, 0777)
		if err != nil {
			log.Fatalf("error opening log file: %v", err)
		}
		logger = slog.New(slogmulti.Fanout(
			slog.NewTextHandler(os.Stdout, nil),
			slog.NewJSONHandler(f, nil),
		))
	} else {
		logger = slog.New(slog.NewTextHandler(os.Stdout, nil))
	}

	logger.Info("Auto-start servers", "autostart", autoStartServers)
	logger.Info("Using config file", "config", configFilepath)
	logger.Info("Using banned clients file", "bannedFile", bannedFilePath)
	logger.Info("Using log folder", "logFolder", logFolder)
	logger.Info("File logging enabled", "fileLogEnabled", fileLogEnabled)
	logger.Info("Version", "version", app.Version)
	if isHeadless {
		logger.Info("Distribution mode", "mode", distributionMode)
	} else {
		logger.Info("Running in GUI mode")
	}

	return
}
