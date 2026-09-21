package logging

import (
	"fmt"
	"log"
	"os"
	"strings"
)

type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

var levelNames = map[Level]string{
	LevelDebug: "DEBUG",
	LevelInfo:  "INFO",
	LevelWarn:  "WARN",
	LevelError: "ERROR",
}

var currentLevel = LevelInfo

func Init() {
	switch strings.ToLower(os.Getenv("NODE_LOG_LEVEL")) {
	case "debug":
		currentLevel = LevelDebug
	case "info":
		currentLevel = LevelInfo
	case "warn":
		currentLevel = LevelWarn
	case "error":
		currentLevel = LevelError
	}
	log.SetFlags(log.Ldate | log.Ltime)
}

func logf(level Level, format string, args ...any) {
	if level < currentLevel {
		return
	}
	msg := fmt.Sprintf(format, args...)
	log.Printf("[%s] %s", levelNames[level], msg)
}

func Debug(format string, args ...any) { logf(LevelDebug, format, args...) }
func Info(format string, args ...any)  { logf(LevelInfo, format, args...) }
func Warn(format string, args ...any)  { logf(LevelWarn, format, args...) }
func Error(format string, args ...any) { logf(LevelError, format, args...) }
