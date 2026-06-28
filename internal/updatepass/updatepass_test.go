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
	commander := &fakeCommander{selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}}, hasSelf: true, imageIDs: map[string][]string{
		"system.yml/watchlet": {"sha256:old-watchlet", "sha256:new-watchlet"},
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
		"image:system.yml/watchlet", "image:system.yml/nginx",
		"pull:system.yml/nginx", "image:system.yml/nginx",
		"image:main.yml/sonarr", "pull:main.yml/sonarr", "image:main.yml/sonarr",
		"pull:system.yml/watchlet", "image:system.yml/watchlet", "recreate:system.yml/watchlet", "remove:system.yml:sha256:old-watchlet",
	})
	if !strings.Contains(logs.String(), "reason=self-update-last") || !strings.Contains(logs.String(), "reason=self-update-recreate") {
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
		selfService: dockercompose.SelfService{Service: "updater", ComposePaths: []string{"system.yml"}},
		hasSelf:     true,
		imageIDs: map[string][]string{
			"system.yml/updater": {"sha256:old", "sha256:new"},
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
		"image:system.yml/updater", "image:system.yml/app",
		"pull:system.yml/app", "image:system.yml/app", "recreate:system.yml/app",
		"remove:system.yml:sha256:old",
		"pull:system.yml/updater", "image:system.yml/updater", "recreate:system.yml/updater",
	})
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

func TestRunRetriesFailedPreSelfCleanupAfterSelfRecreate(t *testing.T) {
	parser := &fakeParser{files: map[string]composefile.File{
		"system.yml": {Services: []composefile.Service{
			{ComposePath: "system.yml", Name: "watchlet", Image: "ghcr.io/mmarfil/watchlet:latest"},
			{ComposePath: "system.yml", Name: "helper", Image: "ghcr.io/mmarfil/watchlet:latest"},
		}},
	}}
	commander := &fakeCommander{
		selfService: dockercompose.SelfService{Service: "watchlet", ComposePaths: []string{"system.yml"}},
		hasSelf:     true,
		imageIDs: map[string][]string{
			"system.yml/watchlet": {"sha256:old", "sha256:new"},
			"system.yml/helper":   {"sha256:old", "sha256:new"},
		},
		cleanupErrSeq: map[string][]error{"system.yml:sha256:old": {errors.New("image in use"), nil}},
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
		"image:system.yml/watchlet", "image:system.yml/helper",
		"pull:system.yml/helper", "image:system.yml/helper", "recreate:system.yml/helper",
		"remove:system.yml:sha256:old",
		"pull:system.yml/watchlet", "image:system.yml/watchlet", "recreate:system.yml/watchlet",
		"remove:system.yml:sha256:old",
	})
	for _, want := range []string{"reason=cleanup-failed", "action=cleanup", "status=ok"} {
		if !strings.Contains(logs.String(), want) {
			t.Fatalf("logs missing %q:\n%s", want, logs.String())
		}
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
		imageIDs:      map[string][]string{"one.yml/web": {"sha256:old", "sha256:new"}},
		cleanupErrs:   map[string]error{"one.yml:sha256:old": errors.New("image is in use")},
		pullErrs:      map[string]error{},
		recreateErrs:  map[string]error{},
		imageErrs:     map[string]error{},
		removedImages: []string{},
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
	imageIDs      map[string][]string
	imageErrs     map[string]error
	pullErrs      map[string]error
	recreateErrs  map[string]error
	cleanupErrs   map[string]error
	cleanupErrSeq map[string][]error
	removedImages []string
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

func (f *fakeCommander) RemoveImage(_ context.Context, composePath string, imageID string) error {
	key := composePath + ":" + imageID
	f.calls = append(f.calls, "remove:"+key)
	f.removedImages = append(f.removedImages, imageID)
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
