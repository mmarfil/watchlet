package interval

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"watchlet/internal/config"
	"watchlet/internal/updatepass"
	"watchlet/internal/watchlog"
)

func TestRunRepeatsPassAndSleepsConfiguredInterval(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &fakePassRunner{}
	var sleeps []time.Duration
	sleep := func(_ context.Context, duration time.Duration) error {
		sleeps = append(sleeps, duration)
		if len(sleeps) == 2 {
			cancel()
		}
		return nil
	}
	var logs bytes.Buffer
	cfg := config.Config{ComposePaths: []string{"compose.yml"}, Interval: 30 * time.Minute}

	if err := Run(ctx, cfg, runner, watchlog.New(&logs), sleep); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if runner.calls != 2 {
		t.Fatalf("calls = %d, want 2", runner.calls)
	}
	if len(sleeps) != 2 || sleeps[0] != 30*time.Minute || sleeps[1] != 30*time.Minute {
		t.Fatalf("sleeps = %#v", sleeps)
	}
	if !strings.Contains(logs.String(), "action=pass-end") || !strings.Contains(logs.String(), "action=next-sleep") {
		t.Fatalf("logs missing interval events:\n%s", logs.String())
	}
}

func TestRunContinuesAfterPassFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runner := &fakePassRunner{errs: []error{errors.New("pass failed"), nil}}
	sleepCalls := 0
	sleep := func(_ context.Context, _ time.Duration) error {
		sleepCalls++
		if sleepCalls == 2 {
			cancel()
		}
		return nil
	}
	var logs bytes.Buffer

	err := Run(ctx, config.Config{Interval: time.Hour}, runner, watchlog.New(&logs), sleep)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.calls != 2 {
		t.Fatalf("calls = %d, want 2", runner.calls)
	}
	for _, want := range []string{"action=pass-end", "status=failed", `error="pass failed"`, "status=ok"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunExitsCleanlyWhenSleepIsCanceled(t *testing.T) {
	runner := &fakePassRunner{}
	sleep := func(context.Context, time.Duration) error {
		return context.Canceled
	}

	err := Run(context.Background(), config.Config{Interval: time.Hour}, runner, watchlog.New(&bytes.Buffer{}), sleep)
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if runner.calls != 1 {
		t.Fatalf("calls = %d, want 1", runner.calls)
	}
}

type fakePassRunner struct {
	calls int
	errs  []error
}

func (f *fakePassRunner) Run(context.Context, config.Config) (updatepass.Result, error) {
	f.calls++
	if len(f.errs) == 0 {
		return updatepass.Result{}, nil
	}
	err := f.errs[0]
	f.errs = f.errs[1:]
	return updatepass.Result{}, err
}
