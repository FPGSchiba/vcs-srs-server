package utils

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
	"os"
	"time"
)

var logger *zap.Logger

// ZapLoggerAdapter adapts zap.Logger to implement logger.Logger
type ZapLoggerAdapter struct {
	zapLogger *zap.Logger
}

// NewZapLoggerAdapter creates a new adapter for zap.Logger
func NewZapLoggerAdapter(zapLogger *zap.Logger) *ZapLoggerAdapter {
	return &ZapLoggerAdapter{zapLogger: zapLogger}
}

// Print implements logger.Logger.Print
func (a *ZapLoggerAdapter) Print(message string) {
	a.zapLogger.Info(message)
}

// Trace implements logger.Logger.Trace
func (a *ZapLoggerAdapter) Trace(message string) {
	a.zapLogger.Debug(message) // Using Debug for Trace
}

// Warning implements logger.Logger.Warning
func (a *ZapLoggerAdapter) Warning(message string) {
	a.zapLogger.Warn(message)
}

// Debug implements logger.Logger.Debug
func (a *ZapLoggerAdapter) Debug(message string) {
	a.zapLogger.Debug(message)
}

// Info implements logger.Logger.Info
func (a *ZapLoggerAdapter) Info(message string) {
	a.zapLogger.Info(message)
}

// Error implements logger.Logger.Error
func (a *ZapLoggerAdapter) Error(message string) {
	a.zapLogger.Error(message)
}

// Fatal implements logger.Logger.Error
func (a *ZapLoggerAdapter) Fatal(message string) {
	a.zapLogger.Fatal(message)
}

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
