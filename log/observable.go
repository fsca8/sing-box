package log

import (
	"context"
	"io"
	"os"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/observable"
	"github.com/sagernet/sing/service/filemanager"
)

var _ ObservableFactory = (*defaultFactory)(nil)

type defaultFactory struct {
	ctx               context.Context
	formatter         Formatter
	platformFormatter Formatter
	writer            io.Writer
	file              *os.File
	rotating          io.WriteCloser
	filePath          string
	rotateSize        int64
	platformWriters   atomic.Pointer[[]PlatformWriter]
	needObservable    bool
	level             Level
	subscriber        *observable.Subscriber[Entry]
	observer          *observable.Observer[Entry]
}

func NewDefaultFactory(
	ctx context.Context,
	formatter Formatter,
	writer io.Writer,
	filePath string,
	platformWriter PlatformWriter,
	needObservable bool,
) ObservableFactory {
	return NewDefaultFactoryWithRotation(
		ctx, formatter, writer, filePath, platformWriter, needObservable, 0,
	)
}

// NewDefaultFactoryWithRotation 是带文件轮转上限的版本（fork 新增）。
// maxFileSize <= 0 表示不轮转（等价于 NewDefaultFactory）。
func NewDefaultFactoryWithRotation(
	ctx context.Context,
	formatter Formatter,
	writer io.Writer,
	filePath string,
	platformWriter PlatformWriter,
	needObservable bool,
	maxFileSize int64,
) ObservableFactory {
	factory := &defaultFactory{
		ctx:       ctx,
		formatter: formatter,
		platformFormatter: Formatter{
			BaseTime:         formatter.BaseTime,
			DisableLineBreak: true,
		},
		writer:         writer,
		filePath:       filePath,
		needObservable: needObservable,
		level:          LevelTrace,
		rotateSize:     maxFileSize,
		subscriber:     observable.NewSubscriber[Entry](128),
	}
	if platformWriter != nil {
		factory.platformWriters.Store(&[]PlatformWriter{platformWriter})
	}
	/*if platformWriter != nil {
		factory.platformFormatter.DisableColors = platformWriter.DisableColors()
	}*/
	return factory
}

func (f *defaultFactory) Start() error {
	if f.filePath != "" {
		logFile, err := filemanager.OpenFile(f.ctx, f.filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if f.rotateSize > 0 {
			rotating, rerr := NewRotatingWriter(
				f.filePath,
				f.rotateSize,
				3,
				func(path string, flag int, perm os.FileMode) (*os.File, error) {
					return filemanager.OpenFile(f.ctx, path, flag, perm)
				},
			)
			if rerr != nil {
				return rerr
			}
			// NewRotatingWriter 已自行打开文件，Start 打开的句柄直接丢弃
			_ = logFile.Close()
			// 统一线格式转换（fork）：sing-box 原生行 → `ts |L| engine|tag|msg`
			f.writer = &unifiedWriter{w: rotating}
			f.rotating = rotating
		} else {
			f.writer = logFile
			f.file = logFile
		}
	}
	if f.needObservable {
		f.observer = observable.NewObserver[Entry](f.subscriber, 64)
	}
	return nil
}

func (f *defaultFactory) Close() error {
	var closers []io.Closer
	if f.rotating != nil {
		closers = append(closers, f.rotating)
	} else if f.file != nil {
		closers = append(closers, f.file)
	}
	closers = append(closers, f.subscriber)
	args := make([]any, 0, len(closers))
	for _, c := range closers {
		args = append(args, c)
	}
	return common.Close(args...)
}

func (f *defaultFactory) AttachPlatformWriter(writer PlatformWriter) {
	writers := append(f.loadPlatformWriters(), writer)
	f.platformWriters.Store(&writers)
}

func (f *defaultFactory) loadPlatformWriters() []PlatformWriter {
	writers := f.platformWriters.Load()
	if writers == nil {
		return nil
	}
	return *writers
}

func (f *defaultFactory) Level() Level {
	return f.level
}

func (f *defaultFactory) SetLevel(level Level) {
	f.level = level
}

func (f *defaultFactory) Logger() ContextLogger {
	return f.NewLogger("")
}

func (f *defaultFactory) NewLogger(tag string) ContextLogger {
	return &observableLogger{f, tag}
}

func (f *defaultFactory) Subscribe() (subscription observable.Subscription[Entry], done <-chan struct{}, err error) {
	return f.observer.Subscribe()
}

func (f *defaultFactory) UnSubscribe(sub observable.Subscription[Entry]) {
	f.observer.UnSubscribe(sub)
}

var _ ContextLogger = (*observableLogger)(nil)

type observableLogger struct {
	*defaultFactory
	tag string
}

func (l *observableLogger) Log(ctx context.Context, level Level, args []any) {
	level = OverrideLevelFromContext(level, ctx)
	platformWriters := l.loadPlatformWriters()
	if level > l.level && len(platformWriters) == 0 && !l.needObservable {
		return
	}
	nowTime := time.Now()
	if l.needObservable {
		message, messageSimple := l.formatter.FormatWithSimple(ctx, level, l.tag, F.ToString(args...), nowTime)
		if level <= l.level {
			if level == LevelPanic {
				panic(message)
			}
			l.writer.Write([]byte(message))
			if level == LevelFatal {
				os.Exit(1)
			}
		}
		l.subscriber.Emit(Entry{level, messageSimple})
	} else if level <= l.level {
		message := l.formatter.Format(ctx, level, l.tag, F.ToString(args...), nowTime)
		if level == LevelPanic {
			panic(message)
		}
		l.writer.Write([]byte(message))
		if level == LevelFatal {
			os.Exit(1)
		}
	}
	if len(platformWriters) > 0 {
		message := l.platformFormatter.Format(ctx, level, l.tag, F.ToString(args...), nowTime)
		for _, platformWriter := range platformWriters {
			platformWriter.WriteMessage(level, message)
		}
	}
}

func (l *observableLogger) Trace(args ...any) {
	l.TraceContext(context.Background(), args...)
}

func (l *observableLogger) Debug(args ...any) {
	l.DebugContext(context.Background(), args...)
}

func (l *observableLogger) Info(args ...any) {
	l.InfoContext(context.Background(), args...)
}

func (l *observableLogger) Warn(args ...any) {
	l.WarnContext(context.Background(), args...)
}

func (l *observableLogger) Error(args ...any) {
	l.ErrorContext(context.Background(), args...)
}

func (l *observableLogger) Fatal(args ...any) {
	l.FatalContext(context.Background(), args...)
}

func (l *observableLogger) Panic(args ...any) {
	l.PanicContext(context.Background(), args...)
}

func (l *observableLogger) TraceContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelTrace, args)
}

func (l *observableLogger) DebugContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelDebug, args)
}

func (l *observableLogger) InfoContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelInfo, args)
}

func (l *observableLogger) WarnContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelWarn, args)
}

func (l *observableLogger) ErrorContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelError, args)
}

func (l *observableLogger) FatalContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelFatal, args)
}

func (l *observableLogger) PanicContext(ctx context.Context, args ...any) {
	l.Log(ctx, LevelPanic, args)
}
