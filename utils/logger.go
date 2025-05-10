package utils

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"os"
	"time"
)

var logger *zap.Logger

func CreateLogger() *zap.Logger {
	stdout := zapcore.AddSync(os.Stdout)

	file := zapcore.AddSync(&lumberjack.Logger{
		Filename:   "log/vcs-server.log",
		MaxSize:    10, // megabytes
		MaxBackups: 3,
		MaxAge:     180, // days
	})

	level := zap.NewAtomicLevelAt(zap.InfoLevel)

	// Custom encoder config for both console and file
	customEncoderConfig := zapcore.EncoderConfig{
		TimeKey:        "timestamp",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder, // ERRO, INFO, etc
		EncodeTime:     customTimeEncoder,           // Custom time format
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	consoleEncoder := zapcore.NewConsoleEncoder(customEncoderConfig)
	fileEncoder := zapcore.NewJSONEncoder(customEncoderConfig)

	core := zapcore.NewTee(
		zapcore.NewCore(consoleEncoder, stdout, level),
		zapcore.NewCore(fileEncoder, file, level),
	)

	// Enable caller tracking
	logger := zap.New(core, zap.AddCaller())

	return logger
}

// Custom time encoder function
func customTimeEncoder(t time.Time, enc zapcore.PrimitiveArrayEncoder) {
	enc.AppendString(t.Format("2006-01-02 15:04:05"))
}

func GetLogger() *zap.Logger {
	if logger == nil {
		logger = CreateLogger()
	}
	return logger
}
