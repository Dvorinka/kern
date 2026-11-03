// Package telemetry configures structured logging.
package telemetry

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// NewLogger returns a JSON logger in production and a human-readable one elsewhere.
func NewLogger(env, component string) (*zap.Logger, error) {
	var cfg zap.Config
	if env == "development" {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
	}
	cfg.OutputPaths = []string{"stdout"}
	cfg.ErrorOutputPaths = []string{"stderr"}
	logger, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return logger.With(zap.String("service", component), zap.String("pid", pid())), nil
}

func pid() string {
	return os.Getenv("KERN_INSTANCE_ID")
}
