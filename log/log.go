package log

import (
	"context"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

// 引擎文件日志轮转上限由 log.max_file_size_mb 配置控制（见 log.New），
// 不再使用默认常量。

type Options struct {
	Context        context.Context
	Options        option.LogOptions
	Observable     bool
	DefaultWriter  io.Writer
	BaseTime       time.Time
	PlatformWriter PlatformWriter
}

func New(options Options) (Factory, error) {
	logOptions := options.Options

	if logOptions.Disabled {
		return NewNOPFactory(), nil
	}

	var logWriter io.Writer
	var logFilePath string

	switch logOptions.Output {
	case "":
		logWriter = options.DefaultWriter
		if logWriter == nil {
			logWriter = os.Stderr
		}
	case "stderr":
		logWriter = os.Stderr
	case "stdout":
		logWriter = os.Stdout
	default:
		logWriter = io.Discard
		logFilePath = logOptions.Output
	}
	logFormatter := Formatter{
		BaseTime:         options.BaseTime,
		DisableColors:    logOptions.DisableColor || logFilePath != "",
		DisableTimestamp: !logOptions.Timestamp && logFilePath != "",
		FullTimestamp:    logOptions.Timestamp,
		TimestampFormat:  "-0700 2006-01-02 15:04:05",
	}
	// 轮转上限（fork）：log.max_file_size_mb > 0 时启用轮转 + 统一格式。
	// 环境变量 SINGBOX_LOG_MAX_MB 可覆盖（MB）。
	maxFileSize := logOptions.MaxFileSizeMB << 20
	if envStr := os.Getenv("SINGBOX_LOG_MAX_MB"); envStr != "" {
		if mb, err := strconv.ParseInt(envStr, 10, 64); err == nil && mb > 0 {
			maxFileSize = mb << 20
		}
	}
	factory := NewDefaultFactoryWithRotation(
		options.Context,
		logFormatter,
		logWriter,
		logFilePath,
		options.PlatformWriter,
		options.Observable,
		maxFileSize,
	)
	if logOptions.Level != "" {
		logLevel, err := ParseLevel(logOptions.Level)
		if err != nil {
			return nil, E.Cause(err, "parse log level")
		}
		factory.SetLevel(logLevel)
	} else {
		factory.SetLevel(LevelTrace)
	}
	return factory, nil
}
