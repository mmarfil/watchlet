package updatepass

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"watchlet/internal/composefile"
	"watchlet/internal/config"
	"watchlet/internal/dockercompose"
	"watchlet/internal/watchlog"
)

func TestRunProcessesCurrentChangedForcedAndSkippedServices(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {
			Path: "one.yml",
			Services: []composefile.Service{
				{ComposePath: "one.yml", Name: "current", Image: "example/current:latest"},
				{ComposePath: "one.yml", Name: "changed", Image: "example/changed:latest"},
			},
			Skipped: []composefile.SkippedService{{ComposePath: "one.yml", Name: "builder", Reason: composefile.SkipLocalBuild}},
		},
		"two.yml": {
			Path:     "two.yml",
			Services: []composefile.Service{{ComposePath: "two.yml", Name: "forced", Image: "example/forced:latest"}},
		},
	}}
	commander := &fakeCommander{imageIDs: map[string][]string{
		"one.yml/current": {"sha256:same", "sha256:same"},
		"one.yml/changed": {"sha256:old", "sha256:new"},
		"two.yml/forced":  {"sha256:same", "sha256:same"},
	}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{
		ComposePaths: []string{"one.yml", "two.yml"},
		Interval:     24 * time.Hour,
		Once:         true,
		Force:        true,
	})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	want := Result{ComposeFiles: 2, Selected: 3, Updated: 1, Forced: 2, Skipped: 1}
	if !reflect.DeepEqual(result, want) {
		t.Fatalf("result = %#v, want %#v", result, want)
	}
	assertCalls(t, commander.calls, []string{
		"image:one.yml/current", "image:one.yml/changed", "pull:one.yml/current", "pull:one.yml/changed", "image:one.yml/current", "recreate-force:one.yml/current", "image:one.yml/changed", "recreate-force:one.yml/changed",
		"image:two.yml/forced", "pull:two.yml/forced", "image:two.yml/forced", "recreate-force:two.yml/forced",
		"remove:one.yml:sha256:old",
	})
	for _, wantLog := range []string{"action=service-skipped", "reason=local-build", "action=compose-result", "action=pass-summary", "forced=1"} {
		if !strings.Contains(logs.String(), wantLog) {
			t.Fatalf("logs missing %q:\n%s", wantLog, logs.String())
		}
	}
}

func TestRunDoesNotRecreateCurrentServiceWithoutForce(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{{ComposePath: "one.yml", Name: "web", Image: "example/web:latest"}}},
	}}
	commander := &fakeCommander{imageIDs: map[string][]string{"one.yml/web": {"sha256:same", "sha256:same"}}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Current != 1 || result.Updated != 0 || result.Forced != 0 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{"image:one.yml/web", "pull:one.yml/web", "image:one.yml/web"})
}

func TestRunTreatsEmptyBeforeAndResolvedAfterAsChanged(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{{ComposePath: "one.yml", Name: "web", Image: "example/web:latest"}}},
	}}
	commander := &fakeCommander{imageIDs: map[string][]string{"one.yml/web": {"", "sha256:new"}}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 1 || result.Current != 0 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{"image:one.yml/web", "pull:one.yml/web", "image:one.yml/web", "recreate:one.yml/web"})
}

func TestRunFailsWhenImageIdentityIsUnavailableAfterPull(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{{ComposePath: "one.yml", Name: "web", Image: "example/web:latest"}}},
	}}
	commander := &fakeCommander{imageIDs: map[string][]string{"one.yml/web": {"", ""}}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 || result.Current != 0 || result.Updated != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{"image:one.yml/web", "pull:one.yml/web", "image:one.yml/web"})
	if !strings.Contains(logs.String(), "reason=image-inspect-failed") || !strings.Contains(logs.String(), "image identity unavailable after pull") {
		t.Fatalf("logs missing post-pull image identity failure:\n%s", logs.String())
	}
}

func TestRunRecordsAllBeforeIdentitiesBeforePullingSharedImages(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{
			{ComposePath: "one.yml", Name: "web", Image: "example/app:latest"},
			{ComposePath: "one.yml", Name: "worker", Image: "example/app:latest"},
		}},
	}}
	commander := &fakeCommander{imageIDs: map[string][]string{
		"one.yml/web":    {"sha256:old", "sha256:new"},
		"one.yml/worker": {"sha256:old", "sha256:new"},
	}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 2 || result.Current != 0 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"image:one.yml/web", "image:one.yml/worker",
		"pull:one.yml/web", "pull:one.yml/worker",
		"image:one.yml/web", "recreate:one.yml/web",
		"image:one.yml/worker", "recreate:one.yml/worker",
		"remove:one.yml:sha256:old",
	})
}

func TestRunDeduplicatesFailedCleanupWithinCleanupPhase(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{
			{ComposePath: "one.yml", Name: "web", Image: "example/app:latest"},
			{ComposePath: "one.yml", Name: "worker", Image: "example/app:latest"},
		}},
	}}
	commander := &fakeCommander{
		imageIDs: map[string][]string{
			"one.yml/web":    {"sha256:old", "sha256:new"},
			"one.yml/worker": {"sha256:old", "sha256:new"},
		},
		cleanupErrs: map[string]error{"one.yml:sha256:old": errors.New("image in use")},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"image:one.yml/web", "image:one.yml/worker",
		"pull:one.yml/web", "pull:one.yml/worker",
		"image:one.yml/web", "recreate:one.yml/web",
		"image:one.yml/worker", "recreate:one.yml/worker",
		"remove:one.yml:sha256:old",
	})
}

func TestRunDefersWatchletSelfUpdateUntilEndOfPass(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"},
			{ComposePath: "system.yml", Name: "nginx", Image: "jc21/nginx-proxy-manager"},
		}},
		"main.yml": {Services: []composefile.Service{
			{ComposePath: "main.yml", Name: "sonarr", Image: "lscr.io/linuxserver/sonarr"},
		}},
	}}
	commander := &fakeCommander{selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:old-watchlet"}, hasSelf: true, imageIDs: map[string][]string{
		"system.yml/watchlet": {"sha256:new-watchlet"},
		"system.yml/nginx":    {"sha256:same-nginx", "sha256:same-nginx"},
		"main.yml/sonarr":     {"sha256:same-sonarr", "sha256:same-sonarr"},
	}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml", "main.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Selected != 3 || result.Updated != 1 || result.Current != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect",
		"image:system.yml/nginx",
		"pull:system.yml/nginx", "image:system.yml/nginx",
		"image:main.yml/sonarr", "pull:main.yml/sonarr", "image:main.yml/sonarr",
		"pull:system.yml/watchlet", "image:system.yml/watchlet", "handoff:system.yml/watchlet:old=sha256:old-watchlet",
	})
	if !strings.Contains(logs.String(), "reason=self-update-last") || !strings.Contains(logs.String(), "reason=self-update-recreate") || !strings.Contains(logs.String(), "status=handed-off") {
		t.Fatalf("logs missing self-update ordering markers:\n%s", logs.String())
	}
}

func TestRunDefersOnlyCurrentWatchletService(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "updater", Image: "${WATCHLET_IMAGE}:latest"},
			{ComposePath: "system.yml", Name: "app", Image: "ghcr.io/mmarfil/watchlet:latest"},
		}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "updater", ComposePaths: []string{"system.yml"}, ImageID: "sha256:old"},
		hasSelf:     true,
		imageIDs: map[string][]string{
			"system.yml/updater": {"sha256:new"},
			"system.yml/app":     {"sha256:old", "sha256:new"},
		},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect",
		"image:system.yml/app",
		"pull:system.yml/app", "image:system.yml/app", "recreate:system.yml/app",
		"remove:system.yml:sha256:old",
		"pull:system.yml/updater", "image:system.yml/updater", "handoff:system.yml/updater:old=sha256:old",
	})
}

func TestRunForceSkipsUnchangedWatchletSelfUpdate(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"},
			{ComposePath: "system.yml", Name: "app", Image: "example/app:latest"},
		}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:same"},
		hasSelf:     true,
		imageIDs: map[string][]string{
			"system.yml/watchlet": {"sha256:same"},
			"system.yml/app":      {"sha256:app", "sha256:app"},
		},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true, Force: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Forced != 1 || result.Current != 1 || result.Updated != 0 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect",
		"image:system.yml/app", "pull:system.yml/app", "image:system.yml/app", "recreate-force:system.yml/app",
		"pull:system.yml/watchlet", "image:system.yml/watchlet",
	})
	if !strings.Contains(logs.String(), "service=watchlet status=skipped reason=force-skips-self") {
		t.Fatalf("logs missing force-skips-self:\n%s", logs.String())
	}
}

func TestRunForceHandsOffChangedWatchletSelfUpdate(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:old"},
		hasSelf:     true,
		imageIDs:    map[string][]string{"system.yml/watchlet": {"sha256:new"}},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true, Force: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 1 || result.Forced != 0 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect", "pull:system.yml/watchlet", "image:system.yml/watchlet", "handoff-force:system.yml/watchlet:old=sha256:old",
	})
}

func TestRunRetriesSelfUpdateWhenRunningImageIsBehindTag(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:v1"},
		hasSelf:     true,
		// The tag already points at v2 from an earlier pass whose handoff failed.
		imageIDs: map[string][]string{"system.yml/watchlet": {"sha256:v2"}},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 1 || result.Current != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect", "pull:system.yml/watchlet", "image:system.yml/watchlet", "handoff:system.yml/watchlet:old=sha256:v1",
	})
}

func TestRunReportsSelfUpdateHandoffFailureAsRecreateFailure(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:old"},
		hasSelf:     true,
		imageIDs:    map[string][]string{"system.yml/watchlet": {"sha256:new"}},
		handoffErr:  errors.New("Conflict. The container name \"/watchlet-self-update\" is already in use"),
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 || result.Updated != 0 {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"reason=recreate-failed", "already in use", "action=pass-summary status=failed"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunReportsSuccessfulSelfUpdateAndRemovesPreviousImage(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"},
			{ComposePath: "system.yml", Name: "nginx", Image: "jc21/nginx-proxy-manager"},
		}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:current"},
		hasSelf:     true,
		selfFound:   true,
		selfOutcome: dockercompose.SelfUpdateOutcome{Helper: "watchlet-self-update", ComposePath: "system.yml", Service: "watchlet", OldImageID: "sha256:previous-watchlet"},
		imageIDs: map[string][]string{
			"system.yml/watchlet": {"sha256:current"},
			"system.yml/nginx":    {"sha256:old-nginx", "sha256:new-nginx"},
		},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 1 || result.Current != 1 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect", "remove:system.yml:sha256:previous-watchlet",
		"image:system.yml/nginx",
		"pull:system.yml/nginx", "image:system.yml/nginx", "recreate:system.yml/nginx",
		"remove:system.yml:sha256:old-nginx",
		"pull:system.yml/watchlet", "image:system.yml/watchlet",
	})
	if !strings.Contains(logs.String(), "operation=self-update compose=system.yml service=watchlet helper=watchlet-self-update status=ok") {
		t.Fatalf("logs missing self-update result:\n%s", logs.String())
	}
}

func TestRunReportsFailedSelfUpdateHelperAsPassFailure(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	helperErr := &dockercompose.CommandError{Action: "self-update", ComposePath: "system.yml", Service: "watchlet", Stderr: "port is already allocated", Err: errors.New("helper watchlet-self-update exited with code 1")}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:current"},
		hasSelf:     true,
		selfFound:   true,
		selfOutcome: dockercompose.SelfUpdateOutcome{Helper: "watchlet-self-update", ComposePath: "system.yml", Service: "watchlet", OldImageID: "sha256:previous", Err: helperErr},
		imageIDs:    map[string][]string{"system.yml/watchlet": {"sha256:current"}},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 || result.Current != 1 {
		t.Fatalf("result = %#v", result)
	}
	for _, call := range commander.calls {
		if strings.HasPrefix(call, "remove:") {
			t.Fatalf("failed self-update must not remove the previous image: %#v", commander.calls)
		}
	}
	for _, want := range []string{"status=failed reason=self-update-failed", "exited with code 1", "stderr=\"port is already allocated\""} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunLeavesRunningSelfUpdateHelperForNextPass(t *testing.T) {
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}},
		hasSelf:     true,
		selfFound:   true,
		selfOutcome: dockercompose.SelfUpdateOutcome{Helper: "watchlet-self-update", ComposePath: "system.yml", Service: "watchlet", Pending: true},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: &fakeParser{}, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(logs.String(), "status=pending reason=helper-running") {
		t.Fatalf("logs missing pending helper:\n%s", logs.String())
	}
}

func TestRunCountsSelfUpdateCollectionErrorAsFailure(t *testing.T) {
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}},
		hasSelf:     true,
		collectErr:  errors.New("permission denied"),
	}
	var logs bytes.Buffer
	runner := Runner{Parser: &fakeParser{}, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(logs.String(), "reason=self-update-failed error=\"permission denied\"") {
		t.Fatalf("logs missing collection failure:\n%s", logs.String())
	}
}

func TestRunFailsBeforeUpdatesWhenSelfIdentificationFails(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	commander := &fakeCommander{selfErr: errors.New("inspect failed")}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 || result.Selected != 0 || result.Updated != 0 || result.Current != 0 {
		t.Fatalf("result = %#v", result)
	}
	if len(commander.calls) != 0 {
		t.Fatalf("calls = %#v, want none", commander.calls)
	}
	for _, want := range []string{"action=failure", "reason=self-identify", "action=pass-summary", "status=failed", "failed=1"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunDoesNotRetryCleanupAfterSelfUpdateHandoff(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"},
			{ComposePath: "system.yml", Name: "helper", Image: "ghcr.io/mmarfil/watchlet:latest"},
		}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}, ImageID: "sha256:old"},
		hasSelf:     true,
		imageIDs: map[string][]string{
			"system.yml/watchlet": {"sha256:new"},
			"system.yml/helper":   {"sha256:old", "sha256:new"},
		},
		cleanupErrs: map[string]error{"system.yml:sha256:old": errors.New("image in use")},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 2 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{
		"collect",
		"image:system.yml/helper",
		"pull:system.yml/helper", "image:system.yml/helper", "recreate:system.yml/helper",
		"remove:system.yml:sha256:old",
		"pull:system.yml/watchlet", "image:system.yml/watchlet", "handoff:system.yml/watchlet:old=sha256:old",
	})
	if got := strings.Count(logs.String(), "reason=cleanup-failed"); got != 1 {
		t.Fatalf("cleanup-failed logged %d times, want 1:\n%s", got, logs.String())
	}
}

func TestRunLogsDeferredSelfUpdateFailureInComposeResult(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"}}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}},
		hasSelf:     true,
		imageIDs:    map[string][]string{"system.yml/watchlet": {"sha256:old", "sha256:new"}},
		pullErrs:    map[string]error{"system.yml/watchlet": errors.New("pull failed")},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"system.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Selected != 1 || result.Failed != 1 {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"action=compose-result", "compose=system.yml", "status=failed", "selected=1", "failed=1"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunContinuesAfterComposeParseFailure(t *testing.T) {
	parser := &fakeParser{
		files: map[string]composefile.File{
			"good.yml": {Services: []composefile.Service{{ComposePath: "good.yml", Name: "web", Image: "example/web:latest"}}},
		},
		errs: map[string]error{"bad.yml": errors.New("bad yaml")},
	}
	commander := &fakeCommander{imageIDs: map[string][]string{"good.yml/web": {"sha256:same", "sha256:same"}}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"bad.yml", "good.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 || result.Selected != 1 || result.Current != 1 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(logs.String(), "reason=compose-parse") || !strings.Contains(logs.String(), "compose=bad.yml") {
		t.Fatalf("logs missing compose parse failure:\n%s", logs.String())
	}
}

func TestRunCleanupFailureIsLoggedButNonFatal(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{{ComposePath: "one.yml", Name: "web", Image: "example/web:latest"}}},
	}}
	commander := &fakeCommander{
		imageIDs:     map[string][]string{"one.yml/web": {"sha256:old", "sha256:new"}},
		cleanupErrs:  map[string]error{"one.yml:sha256:old": errors.New("image is in use")},
		pullErrs:     map[string]error{},
		recreateErrs: map[string]error{},
		imageErrs:    map[string]error{},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if result.Updated != 1 || result.Failed != 0 {
		t.Fatalf("result = %#v", result)
	}
	assertCalls(t, commander.calls, []string{"image:one.yml/web", "pull:one.yml/web", "image:one.yml/web", "recreate:one.yml/web", "remove:one.yml:sha256:old"})
	for _, want := range []string{"action=cleanup", "compose=one.yml", "service=web", "image_id=sha256:old", "reason=cleanup-failed"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunContinuesAfterServiceFailures(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Services: []composefile.Service{
			{ComposePath: "one.yml", Name: "pullfail", Image: "example/pullfail:latest"},
			{ComposePath: "one.yml", Name: "recreatefail", Image: "example/recreatefail:latest"},
		}},
	}}
	commander := &fakeCommander{
		imageIDs: map[string][]string{
			"one.yml/pullfail":     {"sha256:old", "sha256:new"},
			"one.yml/recreatefail": {"sha256:old", "sha256:new"},
		},
		pullErrs:     map[string]error{"one.yml/pullfail": errors.New("pull failed")},
		recreateErrs: map[string]error{"one.yml/recreatefail": errors.New("recreate failed")},
	}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: commander, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Selected != 2 || result.Failed != 2 {
		t.Fatalf("result = %#v", result)
	}
	for _, want := range []string{"reason=pull-failed", "reason=recreate-failed"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
	}
}

func TestRunCountsInvalidSelectedServicesAsFailures(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"one.yml": {Invalid: []composefile.InvalidService{{ComposePath: "one.yml", Name: "bad", Reason: "selected service must define image or build"}}},
	}}
	var logs bytes.Buffer
	runner := Runner{Parser: parser, Commander: &fakeCommander{}, Logger: watchlog.New(&logs)}

	result, err := runner.Run(context.Background(), config.Config{ComposePaths: []string{"one.yml"}, Interval: 24 * time.Hour, Once: true})
	if err == nil {
		t.Fatal("expected pass error")
	}
	if result.Failed != 1 {
		t.Fatalf("result = %#v", result)
	}
	if !strings.Contains(logs.String(), "reason=invalid-selected-service") {
		t.Fatalf("logs missing invalid skip reason:\n%s", logs.String())
	}
}

type fakeParser struct {
	files map[string]composefile.File
	errs  map[string]error
}

func (f *fakeParser) Load(path string) (composefile.File, error) {
	if err := f.errs[path]; err != nil {
		return composefile.File{}, err
	}
	return f.files[path], nil
}

type fakeCommander struct {
	calls         []string
	selfService   dockercompose.SelfService
	hasSelf       bool
	selfErr       error
	selfOutcome   dockercompose.SelfUpdateOutcome
	selfFound     bool
	collectErr    error
	handoffErr    error
	imageIDs      map[string][]string
	imageErrs     map[string]error
	pullErrs      map[string]error
	recreateErrs  map[string]error
	cleanupErrs   map[string]error
	cleanupErrSeq map[string][]error
}

func (f *fakeCommander) CurrentService(context.Context) (dockercompose.SelfService, bool, error) {
	return f.selfService, f.hasSelf, f.selfErr
}

func (f *fakeCommander) ImageID(_ context.Context, composePath string, service string) (string, error) {
	key := composePath + "/" + service
	f.calls = append(f.calls, "image:"+key)
	if err := f.imageErrs[key]; err != nil {
		return "", err
	}
	ids := f.imageIDs[key]
	if len(ids) == 0 {
		return "", nil
	}
	id := ids[0]
	f.imageIDs[key] = ids[1:]
	return id, nil
}

func (f *fakeCommander) Pull(_ context.Context, composePath string, service string) error {
	key := composePath + "/" + service
	f.calls = append(f.calls, "pull:"+key)
	return f.pullErrs[key]
}

func (f *fakeCommander) Recreate(_ context.Context, composePath string, service string, force bool) error {
	key := composePath + "/" + service
	if force {
		f.calls = append(f.calls, "recreate-force:"+key)
	} else {
		f.calls = append(f.calls, "recreate:"+key)
	}
	return f.recreateErrs[key]
}

func (f *fakeCommander) HandOffSelfRecreate(_ context.Context, _ dockercompose.SelfService, composePath string, service string, force bool, oldImageID string) (string, error) {
	call := "handoff:"
	if force {
		call = "handoff-force:"
	}
	f.calls = append(f.calls, call+composePath+"/"+service+":old="+oldImageID)
	return service + "-self-update", f.handoffErr
}

func (f *fakeCommander) CollectSelfUpdate(context.Context, dockercompose.SelfService) (dockercompose.SelfUpdateOutcome, bool, error) {
	f.calls = append(f.calls, "collect")
	return f.selfOutcome, f.selfFound, f.collectErr
}

func (f *fakeCommander) RemoveImage(_ context.Context, composePath string, imageID string) error {
	key := composePath + ":" + imageID
	f.calls = append(f.calls, "remove:"+key)
	if errs := f.cleanupErrSeq[key]; len(errs) > 0 {
		err := errs[0]
		f.cleanupErrSeq[key] = errs[1:]
		return err
	}
	return f.cleanupErrs[key]
}

func assertCalls(t *testing.T, got []string, want []string) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("calls = %#v, want %#v", got, want)
	}
}
