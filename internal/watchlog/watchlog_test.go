package watchlog

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestLoggerEmitsPassContextAndSummary(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	logger.PassStart("24h0m0s", false, false, 2)
	logger.ComposeConfigured("/stacks/app.yml")
	logger.PassSummary("ok", 2, 3, 1, 1, 0, 1, 0)

	logs := buf.String()
	assertContains(t, logs, "level=INFO")
	assertContains(t, logs, "action=pass-start")
	assertContains(t, logs, "interval=24h0m0s")
	assertContains(t, logs, "compose_count=2")
	assertContains(t, logs, "compose_files=2")
	assertContains(t, logs, "action=compose-configured")
	assertContains(t, logs, "compose=/stacks/app.yml")
	assertContains(t, logs, "action=pass-summary")
	assertContains(t, logs, "status=ok")
}

func TestLoggerEmitsPassEndAndNextSleep(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	logger.PassEnd("failed", errors.New("pass failed"))
	logger.NextSleep("24h0m0s")

	logs := buf.String()
	assertContains(t, logs, "action=pass-end")
	assertContains(t, logs, "status=failed")
	assertContains(t, logs, `error="pass failed"`)
	assertContains(t, logs, "action=next-sleep")
	assertContains(t, logs, "duration=24h0m0s")
}

func TestLoggerEmitsServiceAndActionFields(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	logger.ServiceSelected("/stacks/app.yml", "web", "example/web:latest")
	logger.ServiceSkipped("/stacks/app.yml", "builder", "local-build")
	logger.ActionResult("pull", "/stacks/app.yml", "web", "failed", "pull-failed", diagnosticError{message: "exit status 1", stdout: "pulled partial", stderr: "auth denied"})

	logs := buf.String()
	assertContains(t, logs, "action=service-selected")
	assertContains(t, logs, "compose=/stacks/app.yml")
	assertContains(t, logs, "service=web")
	assertContains(t, logs, "image=example/web:latest")
	assertContains(t, logs, "action=service-skipped")
	assertContains(t, logs, "service=builder")
	assertContains(t, logs, "reason=local-build")
	assertContains(t, logs, "action=action-result")
	assertContains(t, logs, "status=failed")
	assertContains(t, logs, "reason=pull-failed")
	assertContains(t, logs, `error="exit status 1"`)
	assertContains(t, logs, `stdout="pulled partial"`)
	assertContains(t, logs, `stderr="auth denied"`)
}

func TestLoggerEmitsCleanupResult(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	logger.CleanupResult("/stacks/app.yml", "web", "sha256:old", "skipped", "cleanup-failed", errors.New("image is in use"))

	logs := buf.String()
	assertContains(t, logs, "action=cleanup")
	assertContains(t, logs, "compose=/stacks/app.yml")
	assertContains(t, logs, "service=web")
	assertContains(t, logs, "image_id=sha256:old")
	assertContains(t, logs, "status=skipped")
	assertContains(t, logs, "reason=cleanup-failed")
	assertContains(t, logs, `error="image is in use"`)
}

func TestLoggerEmitsFailureLayer(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf)

	logger.Failure("compose-parse", "/stacks/app.yml", "", errors.New("bad yaml"))

	logs := buf.String()
	assertContains(t, logs, "action=failure")
	assertContains(t, logs, "reason=compose-parse")
	assertContains(t, logs, "compose=/stacks/app.yml")
	assertContains(t, logs, `error="bad yaml"`)
}

type diagnosticError struct {
	message string
	stdout  string
	stderr  string
}

func (e diagnosticError) Error() string { return e.message }

func (e diagnosticError) CommandDiagnostics() (string, string) {
	return e.stdout, e.stderr
}

func assertContains(t *testing.T, value string, want string) {
	t.Helper()
	if !strings.Contains(value, want) {
		t.Fatalf("expected %q to contain %q", value, want)
	}
}
