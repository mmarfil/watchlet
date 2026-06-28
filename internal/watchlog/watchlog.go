package watchlog

import (
	"errors"
	"io"
	"log/slog"
	"strings"
)

type Logger struct {
	logger *slog.Logger
}

type commandDiagnosticError interface {
	CommandDiagnostics() (stdout string, stderr string)
}

func New(w io.Writer) Logger {
	handler := slog.NewTextHandler(w, &slog.HandlerOptions{Level: slog.LevelInfo})
	return Logger{logger: slog.New(handler)}
}

func (l Logger) PassStart(interval string, once bool, force bool, composeCount int) {
	l.event("pass-start",
		"interval", interval,
		"once", once,
		"force", force,
		"compose_count", composeCount,
	)
}

func (l Logger) ComposeConfigured(compose string) {
	l.event("compose-configured", "compose", compose)
}

func (l Logger) PassEnd(status string, err error) {
	attrs := []any{"status", status}
	attrs = appendError(attrs, err)
	l.event("pass-end", attrs...)
}

func (l Logger) NextSleep(duration string) {
	l.event("next-sleep", "duration", duration)
}

func (l Logger) ServiceSelected(compose string, service string, image string) {
	l.event("service-selected",
		"compose", compose,
		"service", service,
		"image", image,
	)
}

func (l Logger) ServiceSkipped(compose string, service string, reason string) {
	l.event("service-skipped",
		"compose", compose,
		"service", service,
		"reason", reason,
	)
}

func (l Logger) ActionResult(action string, compose string, service string, status string, reason string, err error) {
	attrs := []any{
		"operation", action,
		"compose", compose,
		"service", service,
		"status", status,
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	attrs = appendError(attrs, err)
	l.event("action-result", attrs...)
}

func (l Logger) CleanupResult(compose string, service string, imageID string, status string, reason string, err error) {
	attrs := []any{
		"compose", compose,
		"service", service,
		"image_id", imageID,
		"status", status,
	}
	if reason != "" {
		attrs = append(attrs, "reason", reason)
	}
	attrs = appendError(attrs, err)
	l.event("cleanup", attrs...)
}

func (l Logger) ComposeResult(compose string, status string, selected int, skipped int, updated int, current int, forced int, failed int) {
	l.event("compose-result",
		"compose", compose,
		"status", status,
		"selected", selected,
		"skipped", skipped,
		"updated", updated,
		"current", current,
		"forced", forced,
		"failed", failed,
	)
}

func (l Logger) PassSummary(status string, composeCount int, selected int, updated int, current int, forced int, skipped int, failed int) {
	l.event("pass-summary",
		"status", status,
		"compose_files", composeCount,
		"selected", selected,
		"updated", updated,
		"current", current,
		"forced", forced,
		"skipped", skipped,
		"failed", failed,
	)
}

func (l Logger) Failure(reason string, compose string, service string, err error) {
	attrs := []any{
		"reason", reason,
	}
	if compose != "" {
		attrs = append(attrs, "compose", compose)
	}
	if service != "" {
		attrs = append(attrs, "service", service)
	}
	attrs = appendError(attrs, err)
	l.event("failure", attrs...)
}

func (l Logger) event(action string, attrs ...any) {
	l.logger.Info("watchlet", append([]any{"action", action}, attrs...)...)
}

func appendError(attrs []any, err error) []any {
	if err == nil {
		return attrs
	}
	attrs = append(attrs, "error", err.Error())
	var diagnostic commandDiagnosticError
	if errors.As(err, &diagnostic) {
		stdout, stderr := diagnostic.CommandDiagnostics()
		if stdout = strings.TrimSpace(stdout); stdout != "" {
			attrs = append(attrs, "stdout", stdout)
		}
		if stderr = strings.TrimSpace(stderr); stderr != "" {
			attrs = append(attrs, "stderr", stderr)
		}
	}
	return attrs
}
