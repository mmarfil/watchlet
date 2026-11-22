package dockercompose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestPullCommandIsScopedToComposeFile(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)

	if err := client.Pull(context.Background(), "/stacks/app.yml", "web"); err != nil {
		t.Fatalf("Pull returned error: %v", err)
	}

	want := command{name: "docker", args: []string{"compose", "--project-directory", "/stacks", "-f", "/stacks/app.yml", "pull", "web"}}
	assertCommands(t, runner.commands, []command{want})
}

func TestRecreateCommandIsScopedToComposeFile(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)

	if err := client.Recreate(context.Background(), "/stacks/app.yml", "web", false); err != nil {
		t.Fatalf("Recreate returned error: %v", err)
	}

	want := command{name: "docker", args: []string{"compose", "--project-directory", "/stacks", "-f", "/stacks/app.yml", "up", "-d", "--no-deps", "web"}}
	assertCommands(t, runner.commands, []command{want})
}

func TestForceRecreateCommandIncludesForceRecreateFlag(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)

	if err := client.Recreate(context.Background(), "/stacks/app.yml", "web", true); err != nil {
		t.Fatalf("Recreate returned error: %v", err)
	}

	want := command{name: "docker", args: []string{"compose", "--project-directory", "/stacks", "-f", "/stacks/app.yml", "up", "-d", "--no-deps", "--force-recreate", "web"}}
	assertCommands(t, runner.commands, []command{want})
}

func TestCurrentServiceTranslatesComposeConfigPathThroughMounts(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname returned error: %v", err)
	}
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet",
        "com.docker.compose.project.config_files": "/Volumes/Docker/System/docker-compose.yml"
      }
    },
    "Mounts": [
      {"Source":"/Volumes/Docker/System","Destination":"/system"}
    ]
  }
]`}}}
	client := New(runner)

	self, ok, err := client.CurrentService(context.Background())
	if err != nil {
		t.Fatalf("CurrentService returned error: %v", err)
	}
	if !ok {
		t.Fatal("CurrentService ok = false, want true")
	}
	if self.Service != "watchlet" {
		t.Fatalf("Service = %q, want watchlet", self.Service)
	}
	wantPaths := []string{"/Volumes/Docker/System/docker-compose.yml", "/system/docker-compose.yml"}
	if !reflect.DeepEqual(self.ComposePaths, wantPaths) {
		t.Fatalf("ComposePaths = %#v, want %#v", self.ComposePaths, wantPaths)
	}
	assertCommands(t, runner.commands, []command{{name: "docker", args: []string{"container", "inspect", hostname}}})
}

func TestCurrentServiceRecordsContainerIdentityForHandoff(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Id": "abc123",
    "Name": "/watchlet",
    "Image": "sha256:running",
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet",
        "com.docker.compose.project.config_files": "/stacks/docker-compose.yml"
      }
    }
  }
]`}}}
	client := New(runner)

	self, ok, err := client.CurrentService(context.Background())
	if err != nil || !ok {
		t.Fatalf("CurrentService = %#v, %v, %v", self, ok, err)
	}
	if self.ContainerID != "abc123" || self.ContainerName != "watchlet" || self.ImageID != "sha256:running" {
		t.Fatalf("self = %#v", self)
	}
}

func TestHandOffSelfRecreateRunsComposeRecreateInHelperContainer(t *testing.T) {
	withProcessEnviron(t, []string{"WATCHLET_COMPOSE=/stacks/app.yml", "HOSTNAME=abc123", "TIMEZONE=UTC"})
	runner := &fakeRunner{}
	client := New(runner)
	self := SelfService{Service: "watchlet", ContainerID: "abc123", ContainerName: "watchlet", ImageID: "sha256:running"}

	helper, err := client.HandOffSelfRecreate(context.Background(), self, "/stacks/app.yml", "watchlet", false, "sha256:old")
	if err != nil {
		t.Fatalf("HandOffSelfRecreate returned error: %v", err)
	}
	if helper != "watchlet-self-update" {
		t.Fatalf("helper = %q", helper)
	}

	want := []string{
		"run", "-d", "--name", "watchlet-self-update", "--network", "none", "--volumes-from", "abc123",
		"-e", "WATCHLET_COMPOSE", "-e", "TIMEZONE",
		"--label", "watchlet.self-update.compose=/stacks/app.yml",
		"--label", "watchlet.self-update.service=watchlet",
		"--label", "watchlet.self-update.old-image=sha256:old",
		"--entrypoint", "docker", "sha256:running",
	}
	want = append(want, recreateCommand(t, "/stacks/app.yml", "watchlet", false)...)
	assertCommands(t, runner.commands, []command{{name: "docker", args: want}})
}

func TestHandOffSelfRecreateForceWithoutOldImageOmitsCleanupLabel(t *testing.T) {
	withProcessEnviron(t, nil)
	runner := &fakeRunner{}
	client := New(runner)
	self := SelfService{Service: "watchlet", ContainerID: "abc123", ContainerName: "watchlet", ImageID: "sha256:running"}

	if _, err := client.HandOffSelfRecreate(context.Background(), self, "/stacks/app.yml", "watchlet", true, ""); err != nil {
		t.Fatalf("HandOffSelfRecreate returned error: %v", err)
	}

	want := []string{
		"run", "-d", "--name", "watchlet-self-update", "--network", "none", "--volumes-from", "abc123",
		"--label", "watchlet.self-update.compose=/stacks/app.yml",
		"--label", "watchlet.self-update.service=watchlet",
		"--entrypoint", "docker", "sha256:running",
	}
	want = append(want, recreateCommand(t, "/stacks/app.yml", "watchlet", true)...)
	assertCommands(t, runner.commands, []command{{name: "docker", args: want}})
}

func TestHandOffSelfRecreateRefusesIncompleteIdentity(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)

	_, err := client.HandOffSelfRecreate(context.Background(), SelfService{Service: "watchlet"}, "/stacks/app.yml", "watchlet", false, "")
	if err == nil {
		t.Fatal("expected error")
	}
	assertCommands(t, runner.commands, nil)
}

func TestHandOffSelfRecreateRefusesDockerReachedOverNetwork(t *testing.T) {
	withProcessEnviron(t, []string{"DOCKER_HOST=tcp://socket-proxy:2375"})
	runner := &fakeRunner{}
	self := SelfService{Service: "watchlet", ContainerID: "abc123", ContainerName: "watchlet", ImageID: "sha256:running"}

	_, err := New(runner).HandOffSelfRecreate(context.Background(), self, "/stacks/app.yml", "watchlet", false, "sha256:old")
	if err == nil || !strings.Contains(err.Error(), "needs the Docker socket") || !strings.Contains(err.Error(), "tcp://") {
		t.Fatalf("err = %v", err)
	}
	assertCommands(t, runner.commands, nil)
}

func TestHandOffSelfRecreateAcceptsUnixDockerHost(t *testing.T) {
	withProcessEnviron(t, []string{"DOCKER_HOST=unix:///var/run/docker.sock"})
	runner := &fakeRunner{}
	self := SelfService{Service: "watchlet", ContainerID: "abc123", ContainerName: "watchlet", ImageID: "sha256:running"}

	if _, err := New(runner).HandOffSelfRecreate(context.Background(), self, "/stacks/app.yml", "watchlet", false, ""); err != nil {
		t.Fatalf("HandOffSelfRecreate returned error: %v", err)
	}
	if len(runner.commands) != 1 {
		t.Fatalf("commands = %#v", runner.commands)
	}
}

var collectSelf = SelfService{Service: "watchlet", ComposePaths: []string{"/host/app.yml", "/stacks/app.yml"}, ContainerName: "watchlet"}

func listHelpersCommand() command {
	return command{name: "docker", args: []string{"container", "ls", "-a", "--filter", "label=watchlet.self-update.service=watchlet", "--format", "{{.Names}}"}}
}

func dockerCommand(args ...string) command {
	return command{name: "docker", args: args}
}

func TestCollectSelfUpdateReturnsNothingWithoutHelper(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}, {stderr: "Error: No such container: watchlet-self-update", err: errors.New("exit status 1")}}}

	_, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	assertCommands(t, runner.commands, []command{listHelpersCommand(), dockerCommand("container", "inspect", "watchlet-self-update")})
}

func TestCollectSelfUpdateRemovesSuccessfulHelper(t *testing.T) {
	runner := &fakeRunner{responses: []response{{stdout: "watchlet-self-update\n"}, {stdout: helperInspect(false, 0)}, {}}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	want := SelfUpdateOutcome{Helper: "watchlet-self-update", ComposePath: "/stacks/app.yml", Service: "watchlet", OldImageID: "sha256:old"}
	if !reflect.DeepEqual(outcome, want) {
		t.Fatalf("outcome = %#v, want %#v", outcome, want)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "watchlet-self-update"),
		dockerCommand("container", "rm", "watchlet-self-update"),
	})
}

func TestCollectSelfUpdateFindsHelperStartedUnderAnotherContainerName(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: "other-stack-self-update\nsystem-watchlet-run-abc-self-update\n"},
		{stdout: helperInspectFor("/other/app.yml", "exited", false, 0)},
		{stdout: helperInspect(false, 0)},
		{},
	}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found || outcome.Err != nil || outcome.Helper != "system-watchlet-run-abc-self-update" || outcome.OldImageID != "sha256:old" {
		t.Fatalf("outcome=%#v found=%v err=%v", outcome, found, err)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "other-stack-self-update"),
		dockerCommand("container", "inspect", "system-watchlet-run-abc-self-update"),
		dockerCommand("container", "rm", "system-watchlet-run-abc-self-update"),
	})
}

func TestCollectSelfUpdateLeavesUnlabelledContainerUntouched(t *testing.T) {
	runner := &fakeRunner{responses: []response{{}, {stdout: `[{"State":{"Status":"exited","Running":false,"ExitCode":0},"Config":{"Labels":{"app":"backup"}}}]`}}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err == nil || !found || outcome.Helper != "watchlet-self-update" || !strings.Contains(err.Error(), "not a self-update helper for this Watchlet service") {
		t.Fatalf("outcome=%#v found=%v err=%v", outcome, found, err)
	}
	assertCommands(t, runner.commands, []command{listHelpersCommand(), dockerCommand("container", "inspect", "watchlet-self-update")})
}

func TestCollectSelfUpdateReportsFailedHelperWithComposeOutput(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: "watchlet-self-update\n"},
		{stdout: helperInspect(true, 0)},
		{stdout: "1\n"},
		{stderr: "Error response from daemon: port is already allocated"},
		{},
	}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	var commandErr *CommandError
	if !errors.As(outcome.Err, &commandErr) {
		t.Fatalf("outcome.Err = %#v, want *CommandError", outcome.Err)
	}
	if commandErr.Action != "self-update" || commandErr.Stderr != "Error response from daemon: port is already allocated" || !strings.Contains(commandErr.Error(), "exited with code 1") {
		t.Fatalf("CommandError = %#v", commandErr)
	}
	if outcome.OldImageID != "" {
		t.Fatalf("OldImageID = %q, want none after a failed self-update", outcome.OldImageID)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "watchlet-self-update"),
		dockerCommand("container", "wait", "watchlet-self-update"),
		dockerCommand("container", "logs", "watchlet-self-update"),
		dockerCommand("container", "rm", "watchlet-self-update"),
	})
}

func TestCollectSelfUpdateRemovesFailedHelperWhoseLogsCannotBeRead(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: "watchlet-self-update\n"},
		{stdout: helperInspect(false, 1)},
		{stderr: "Error response from daemon: configured logging driver does not support reading", err: errors.New("exit status 1")},
		{},
	}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "exited with code 1") || !strings.Contains(outcome.Err.Error(), "could not be read") || outcome.OldImageID != "" {
		t.Fatalf("outcome = %#v", outcome)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "watchlet-self-update"),
		dockerCommand("container", "logs", "watchlet-self-update"),
		dockerCommand("container", "rm", "watchlet-self-update"),
	})
}

func TestCollectSelfUpdateLeavesHelperRunningPastWait(t *testing.T) {
	oldWait := selfUpdateHelperWait
	selfUpdateHelperWait = time.Nanosecond
	t.Cleanup(func() { selfUpdateHelperWait = oldWait })
	runner := &fakeRunner{responses: []response{{stdout: "watchlet-self-update\n"}, {stdout: helperInspect(true, 0)}, {blockUntilDone: true}}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found || !outcome.Pending {
		t.Fatalf("outcome=%#v found=%v err=%v", outcome, found, err)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "watchlet-self-update"),
		dockerCommand("container", "wait", "watchlet-self-update"),
	})
}

func TestCollectSelfUpdateReportsNeverStartedHelperAsFailure(t *testing.T) {
	runner := &fakeRunner{responses: []response{{stdout: "watchlet-self-update\n"}, {stdout: helperInspectWithStatus("created", false, 0)}, {}}}

	outcome, found, err := New(runner).CollectSelfUpdate(context.Background(), collectSelf)
	if err != nil || !found {
		t.Fatalf("found=%v err=%v", found, err)
	}
	if outcome.Err == nil || !strings.Contains(outcome.Err.Error(), "never ran: status created") {
		t.Fatalf("outcome.Err = %v", outcome.Err)
	}
	if outcome.OldImageID != "" {
		t.Fatalf("OldImageID = %q, want none so the running image is not queued for cleanup", outcome.OldImageID)
	}
	assertCommands(t, runner.commands, []command{
		listHelpersCommand(),
		dockerCommand("container", "inspect", "watchlet-self-update"),
		dockerCommand("container", "rm", "watchlet-self-update"),
	})
}

func TestCollectSelfUpdateFailuresNameTheHelper(t *testing.T) {
	inspectFails := &fakeRunner{responses: []response{{stdout: "watchlet-self-update\n"}, {stderr: "permission denied", err: errors.New("exit status 1")}}}
	outcome, found, err := New(inspectFails).CollectSelfUpdate(context.Background(), collectSelf)
	if err == nil || !found || outcome.Helper != "watchlet-self-update" || strings.Contains(err.Error(), "image=") {
		t.Fatalf("inspect failure: outcome=%#v found=%v err=%v", outcome, found, err)
	}

	removeFails := &fakeRunner{responses: []response{{stdout: "watchlet-self-update\n"}, {stdout: helperInspect(false, 0)}, {stderr: "boom", err: errors.New("exit status 1")}}}
	outcome, _, err = New(removeFails).CollectSelfUpdate(context.Background(), collectSelf)
	if err == nil || outcome.Helper != "watchlet-self-update" || outcome.Service != "watchlet" || strings.Contains(err.Error(), "image=") {
		t.Fatalf("rm failure: outcome=%#v err=%v", outcome, err)
	}
}

func helperInspect(running bool, exitCode int) string {
	status := "exited"
	if running {
		status = "running"
	}
	return helperInspectWithStatus(status, running, exitCode)
}

func helperInspectWithStatus(status string, running bool, exitCode int) string {
	return helperInspectFor("/stacks/app.yml", status, running, exitCode)
}

func helperInspectFor(composePath string, status string, running bool, exitCode int) string {
	return fmt.Sprintf(`[{"State":{"Status":%q,"Running":%t,"ExitCode":%d},"Config":{"Labels":{
  "watchlet.self-update.compose":%q,
  "watchlet.self-update.service":"watchlet",
  "watchlet.self-update.old-image":"sha256:old"}}}]`, status, running, exitCode, composePath)
}

// recreateCommand returns the arguments the normal Recreate path uses, so the
// handoff tests prove the helper runs the identical Compose command.
func recreateCommand(t *testing.T, composePath string, service string, force bool) []string {
	t.Helper()
	runner := &fakeRunner{}
	if err := New(runner).Recreate(context.Background(), composePath, service, force); err != nil {
		t.Fatalf("Recreate returned error: %v", err)
	}
	return runner.commands[0].args
}

func withProcessEnviron(t *testing.T, environ []string) {
	t.Helper()
	old := processEnviron
	processEnviron = func() []string { return environ }
	t.Cleanup(func() { processEnviron = old })
}

func TestCurrentServiceTranslatesAllMatchingMountPathsMostSpecificFirst(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet",
        "com.docker.compose.project.config_files": "/host/stack/docker-compose.yml"
      }
    },
    "Mounts": [
      {"Source":"/host","Destination":"/broad"},
      {"Source":"/host/stack","Destination":"/stack"}
    ]
  }
]`}}}
	client := New(runner)

	self, ok, err := client.CurrentService(context.Background())
	if err != nil {
		t.Fatalf("CurrentService returned error: %v", err)
	}
	if !ok {
		t.Fatal("CurrentService ok = false, want true")
	}
	wantPaths := []string{"/host/stack/docker-compose.yml", "/stack/docker-compose.yml", "/broad/stack/docker-compose.yml"}
	if !reflect.DeepEqual(self.ComposePaths, wantPaths) {
		t.Fatalf("ComposePaths = %#v, want %#v", self.ComposePaths, wantPaths)
	}
}

func TestCurrentServiceReturnsNoSelfLocallyWithoutInspectingHostname(t *testing.T) {
	withContainerDetection(t, nil, nil)
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet",
        "com.docker.compose.project.config_files": "/stacks/docker-compose.yml"
      }
    }
  }
]`}}}
	client := New(runner)

	_, ok, err := client.CurrentService(context.Background())
	if err != nil {
		t.Fatalf("CurrentService returned error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	assertCommands(t, runner.commands, nil)
}

func TestCurrentServiceInspectsCgroupContainerIDBeforeHostname(t *testing.T) {
	cgroup := t.TempDir() + "/cgroup"
	containerID := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	if err := os.WriteFile(cgroup, []byte("0::/docker/"+containerID+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, nil, []string{cgroup})
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet",
        "com.docker.compose.project.config_files": "/stacks/docker-compose.yml"
      }
    }
  }
]`}}}
	client := New(runner)

	self, ok, err := client.CurrentService(context.Background())
	if err != nil {
		t.Fatalf("CurrentService returned error: %v", err)
	}
	if !ok || self.Service != "watchlet" {
		t.Fatalf("self = %#v ok=%v", self, ok)
	}
	assertCommands(t, runner.commands, []command{{name: "docker", args: []string{"container", "inspect", containerID}}})
}

func TestCurrentServiceReturnsErrorWhenContainerComposeLabelsAreIncomplete(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	runner := &fakeRunner{responses: []response{{stdout: `[
  {
    "Config": {
      "Labels": {
        "com.docker.compose.service": "watchlet"
      }
    }
  }
]`}}}
	client := New(runner)

	_, ok, err := client.CurrentService(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if !strings.Contains(err.Error(), "missing Docker Compose service identity labels") {
		t.Fatalf("error = %v", err)
	}
}

func TestCurrentServiceReturnsErrorWhenInspectFails(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	runner := &fakeRunner{responses: []response{{stderr: "permission denied", err: errors.New("exit status 1")}}}
	client := New(runner)

	_, ok, err := client.CurrentService(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want *CommandError", err)
	}
	if commandErr.Action != "self-identify" {
		t.Fatalf("CommandError = %#v", commandErr)
	}
}

func TestCurrentServiceReturnsNoSelfWhenHostnameIsNotAContainerLocally(t *testing.T) {
	withContainerDetection(t, nil, nil)
	runner := &fakeRunner{responses: []response{{stderr: "No such container", err: errors.New("exit status 1")}}}
	client := New(runner)

	_, ok, err := client.CurrentService(context.Background())
	if err != nil {
		t.Fatalf("CurrentService returned error: %v", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
}

func TestCurrentServiceReturnsErrorWhenContainerHostnameIsNotInspectable(t *testing.T) {
	indicator := t.TempDir() + "/.dockerenv"
	if err := os.WriteFile(indicator, nil, 0o644); err != nil {
		t.Fatalf("WriteFile returned error: %v", err)
	}
	withContainerDetection(t, []string{indicator}, nil)
	runner := &fakeRunner{responses: []response{{stderr: "No such container", err: errors.New("exit status 1")}}}
	client := New(runner)

	_, ok, err := client.CurrentService(context.Background())
	if err == nil {
		t.Fatal("expected error")
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want *CommandError", err)
	}
	if commandErr.Action != "self-identify" {
		t.Fatalf("CommandError = %#v", commandErr)
	}
}

func TestImageIDResolvesComposeImageBeforeInspecting(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `{"services":{"web":{"image":"example/web:1.2.3"}}}`, stderr: "WARN obsolete version\n"},
		{stdout: "sha256:abc123\n"},
	}}
	client := New(runner)

	id, err := client.ImageID(context.Background(), "/stacks/app.yml", "web")
	if err != nil {
		t.Fatalf("ImageID returned error: %v", err)
	}
	if id != "sha256:abc123" {
		t.Fatalf("id = %q, want sha256:abc123", id)
	}

	want := []command{
		{name: "docker", args: []string{"compose", "--project-directory", "/stacks", "-f", "/stacks/app.yml", "config", "--format", "json"}},
		{name: "docker", args: []string{"image", "inspect", "--format", "{{.Id}}", "example/web:1.2.3"}},
	}
	assertCommands(t, runner.commands, want)
}

func TestImageIDReturnsEmptyWhenImageIsNotAvailableLocally(t *testing.T) {
	runner := &fakeRunner{responses: []response{
		{stdout: `{"services":{"web":{"image":"example/web:latest"}}}`},
		{stderr: "Error: No such image: example/web:latest", err: errors.New("exit status 1")},
	}}
	client := New(runner)

	id, err := client.ImageID(context.Background(), "/stacks/app.yml", "web")
	if err != nil {
		t.Fatalf("ImageID returned error: %v", err)
	}
	if id != "" {
		t.Fatalf("id = %q, want empty", id)
	}
}

func TestRemoveImageCommandTargetsSpecificImageID(t *testing.T) {
	runner := &fakeRunner{}
	client := New(runner)

	if err := client.RemoveImage(context.Background(), "/stacks/app.yml", "sha256:old"); err != nil {
		t.Fatalf("RemoveImage returned error: %v", err)
	}

	want := command{name: "docker", args: []string{"image", "rm", "sha256:old"}}
	assertCommands(t, runner.commands, []command{want})
}

func TestCommandFailureCarriesActionAndComposeContext(t *testing.T) {
	runner := &fakeRunner{responses: []response{{stdout: "pull failed", stderr: "pull warning", err: errors.New("exit status 1")}}}
	client := New(runner)

	err := client.Pull(context.Background(), "/stacks/app.yml", "web")
	if err == nil {
		t.Fatal("expected error")
	}

	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want *CommandError", err)
	}
	if commandErr.Action != "pull" || commandErr.ComposePath != "/stacks/app.yml" || commandErr.Service != "web" || commandErr.Output != "pull failed" || commandErr.Stderr != "pull warning" {
		t.Fatalf("CommandError = %#v", commandErr)
	}
	message := err.Error()
	for _, want := range []string{"pull failed", "compose=/stacks/app.yml", "service=web", "error=exit status 1"} {
		if !strings.Contains(message, want) {
			t.Fatalf("error message %q does not contain %q", message, want)
		}
	}
}

func TestImageIDEmptyOutputIsACommandError(t *testing.T) {
	runner := &fakeRunner{responses: []response{{stdout: "\n"}}}
	client := New(runner)

	_, err := client.ImageID(context.Background(), "/stacks/app.yml", "web")
	if err == nil {
		t.Fatal("expected error")
	}
	var commandErr *CommandError
	if !errors.As(err, &commandErr) {
		t.Fatalf("error type = %T, want *CommandError", err)
	}
	if commandErr.Action != "image-resolve" {
		t.Fatalf("CommandError = %#v", commandErr)
	}
}

type command struct {
	name string
	args []string
}

type response struct {
	stdout         string
	stderr         string
	err            error
	blockUntilDone bool
}

type fakeRunner struct {
	commands  []command
	responses []response
}

func (f *fakeRunner) Run(ctx context.Context, name string, args ...string) (CommandOutput, error) {
	f.commands = append(f.commands, command{name: name, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return CommandOutput{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
	if response.blockUntilDone {
		<-ctx.Done()
		return CommandOutput{}, ctx.Err()
	}
	return CommandOutput{Stdout: response.stdout, Stderr: response.stderr}, response.err
}

func withContainerDetection(t *testing.T, indicatorPaths []string, cgroupPaths []string) {
	t.Helper()
	oldIndicatorPaths := containerIndicatorPaths
	oldCgroupPaths := containerCgroupPaths
	containerIndicatorPaths = indicatorPaths
	containerCgroupPaths = cgroupPaths
	t.Cleanup(func() {
		containerIndicatorPaths = oldIndicatorPaths
		containerCgroupPaths = oldCgroupPaths
	})
}

func assertCommands(t *testing.T, got []command, want []command) {
	t.Helper()
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("commands = %#v, want %#v", got, want)
	}
}
