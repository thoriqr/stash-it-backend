package logger

import (
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func New(appEnv string) (*zap.Logger, error) {
	if appEnv == "development" {
		return newDevelopment()
	}

	return newProduction()
}

func newDevelopment() (*zap.Logger, error) {
	config := zap.NewDevelopmentConfig()

	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}

	config.EncoderConfig.EncodeTime = zapcore.TimeEncoderOfLayout("15:04:05")
	config.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	config.EncoderConfig.EncodeCaller = zapcore.ShortCallerEncoder

	return config.Build(
		zap.AddStacktrace(zapcore.DPanicLevel),
	)
}

func newProduction() (*zap.Logger, error) {
	config := zap.NewProductionConfig()

	config.OutputPaths = []string{"stdout"}
	config.ErrorOutputPaths = []string{"stderr"}

	return config.Build()
}