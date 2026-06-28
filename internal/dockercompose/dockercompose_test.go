package dockercompose

import (
	"context"
	"errors"
	"os"
	"reflect"
	"strings"
	"testing"
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
	stdout string
	stderr string
	err    error
}

type fakeRunner struct {
	commands  []command
	responses []response
}

func (f *fakeRunner) Run(_ context.Context, name string, args ...string) (CommandOutput, error) {
	f.commands = append(f.commands, command{name: name, args: append([]string(nil), args...)})
	if len(f.responses) == 0 {
		return CommandOutput{}, nil
	}
	response := f.responses[0]
	f.responses = f.responses[1:]
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
