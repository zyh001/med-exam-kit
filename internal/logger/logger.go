// Package logger 提供可配置的日志系统。
// 默认只输出到终端（stderr），配置 log_file 后同时写入文件。
// 日志级别：debug < info < warn < error
package logger

import (
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
)

// Level 日志级别
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func ParseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

// Logger 封装标准库 log.Logger，支持级别过滤和可选文件写入。
type Logger struct {
	mu      sync.Mutex
	level   Level
	std     *log.Logger
	file    *os.File
	fileLog *log.Logger
}

var std = &Logger{level: LevelInfo}

// Init 初始化全局日志器。filePath 为空时只输出到终端。
// 应在程序启动时（读取配置后）调用一次。
func Init(filePath, levelStr string) error {
	std.mu.Lock()
	defer std.mu.Unlock()

	std.level = ParseLevel(levelStr)

	// 终端输出（始终开启）
	flags := log.Ldate | log.Ltime | log.Lmsgprefix
	std.std = log.New(os.Stderr, "", flags)

	// 关闭旧文件（如果有）
	if std.file != nil {
		std.file.Close()
		std.file = nil
		std.fileLog = nil
	}

	if filePath == "" {
		// 重定向标准库 log 到我们的终端 logger
		log.SetOutput(os.Stderr)
		log.SetFlags(flags)
		return nil
	}

	f, err := os.OpenFile(filePath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("logger: 无法打开日志文件 %s: %w", filePath, err)
	}
	std.file = f
	std.fileLog = log.New(f, "", flags)

	// 标准库 log 也写入文件 + 终端
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	log.SetFlags(flags)
	return nil
}

func (l *Logger) log(level Level, prefix, format string, args ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if level < l.level {
		return
	}
	msg := fmt.Sprintf(format, args...)
	line := prefix + msg
	if l.std != nil {
		l.std.Output(3, line) //nolint:errcheck
	} else {
		log.Output(3, line) //nolint:errcheck
	}
	if l.fileLog != nil {
		l.fileLog.Output(3, line) //nolint:errcheck
	}
}

// Close 关闭日志文件（程序退出时调用）。
func Close() {
	std.mu.Lock()
	defer std.mu.Unlock()
	if std.file != nil {
		std.file.Close()
		std.file = nil
	}
}

// 全局便捷函数

func Debugf(format string, args ...any) { std.log(LevelDebug, "[DEBUG] ", format, args...) }
func Infof(format string, args ...any)  { std.log(LevelInfo, "[INFO]  ", format, args...) }
func Warnf(format string, args ...any)  { std.log(LevelWarn, "[WARN]  ", format, args...) }
func Errorf(format string, args ...any) { std.log(LevelError, "[ERROR] ", format, args...) }

func Debug(msg string) { Debugf("%s", msg) }
func Info(msg string)  { Infof("%s", msg) }
func Warn(msg string)  { Warnf("%s", msg) }
func Error(msg string) { Errorf("%s", msg) }
