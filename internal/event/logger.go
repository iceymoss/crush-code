package event

import (
	"fmt"
	"log/slog"

	"github.com/posthog/posthog-go"
)

// _ posthog.Logger = logger{} 是断言，确保 logger 实现了 posthog.Logger 接口
var _ posthog.Logger = logger{}

// logger 实现了 posthog.Logger 接口
type logger struct{}

func (logger) Debugf(format string, args ...any) {
	slog.Debug(fmt.Sprintf(format, args...))
}

func (logger) Logf(format string, args ...any) {
	slog.Info(fmt.Sprintf(format, args...))
}

func (logger) Warnf(format string, args ...any) {
	slog.Warn(fmt.Sprintf(format, args...))
}

func (logger) Errorf(format string, args ...any) {
	slog.Error(fmt.Sprintf(format, args...))
}
