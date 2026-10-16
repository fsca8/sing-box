package log

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/sagernet/sing-box/option"
	E "github.com/sagernet/sing/common/exceptions"
)

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
	// Log file size cap (circular overwrite): default 100 MB, configurable
	// with log.max_size_mb, negative disables the cap.
	var logFileMaxSize int64
	if logFilePath != "" {
		switch {
		case logOptions.MaxSizeMB > 0:
			logFileMaxSize = int64(logOptions.MaxSizeMB) << 20
		case logOptions.MaxSizeMB == 0:
			logFileMaxSize = DefaultLogFileMaxSize
		default:
			logFileMaxSize = 0 // unlimited
		}
	}
	// Repeat folding: within a window only the first repeat_limit lines of the
	// same shape are written verbatim; the rest collapse into one summary line
	// per window. Unset keeps the defaults, a negative value disables folding.
	foldLimit := logOptions.RepeatLimit
	if foldLimit == 0 {
		foldLimit = DefaultFoldLimit
	}
	foldWindow := DefaultFoldWindow
	if logOptions.RepeatWindowSec > 0 {
		foldWindow = time.Duration(logOptions.RepeatWindowSec) * time.Second
	} else if logOptions.RepeatWindowSec < 0 {
		foldLimit = 0
	}
	factory := NewDefaultFactory(
		options.Context,
		logFormatter,
		logWriter,
		logFilePath,
		logFileMaxSize,
		options.PlatformWriter,
		options.Observable,
		FoldOptions{Limit: foldLimit, Window: foldWindow},
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
