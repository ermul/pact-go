package v3

import (
	"fmt"
	"log"
	"os"

	"github.com/hashicorp/logutils"
)

var logFilter *logutils.LevelFilter
var defaultLogLevel = "INFO"

const (
	logLevelTrace logutils.LogLevel = "TRACE"
	logLevelDebug                   = "DEBUG"
	logLevelInfo                    = "INFO"
	logLevelWarn                    = "WARN"
	logLevelError                   = "ERROR"
)

func initLogging() {
	if logFilter == nil {
		logFilter = &logutils.LevelFilter{
			Levels:   []logutils.LogLevel{logLevelTrace, logLevelDebug, logLevelInfo, logLevelWarn, logLevelError},
			MinLevel: logutils.LogLevel(defaultLogLevel),
			Writer:   os.Stderr,
		}
		log.SetOutput(logFilter)
	}
	log.Println("[DEBUG] initialised logging")

}

// SetLogLevel sets the default log level for the Pact framework
func SetLogLevel(level logutils.LogLevel) error {
	switch level {
	case logLevelTrace, logLevelDebug, logLevelError, logLevelInfo, logLevelWarn:
		logFilter.SetMinLevel(level)
		return nil
	default:
		return fmt.Errorf(`invalid logLevel '%s'. Please specify one of "TRACE", "DEBUG", "INFO", "WARN", "ERROR"`, level)
	}
}
