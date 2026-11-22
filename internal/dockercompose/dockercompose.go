package dockercompose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type CommandOutput struct {
	Stdout string
	Stderr string
}

func (o CommandOutput) Combined() string {
	return o.Stdout + o.Stderr
}

type Runner interface {
	Run(ctx context.Context, name string, args ...string) (CommandOutput, error)
}

type ExecRunner struct{}

func (ExecRunner) Run(ctx context.Context, name string, args ...string) (CommandOutput, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return CommandOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

type Client struct {
	runner Runner
}

var containerIndicatorPaths = []string{"/.dockerenv", "/run/.containerenv"}
var containerCgroupPaths = []string{"/proc/1/cgroup", "/proc/self/cgroup"}

type SelfService struct {
	Service       string
	ComposePaths  []string
	ContainerID   string
	ContainerName string
	ImageID       string
}

// SelfUpdateOutcome is what a finished self-update helper left behind.
// Err is set when the helper's Compose recreate failed. Pending means the
// helper was still running after the wait and was left for the next pass.
type SelfUpdateOutcome struct {
	Helper      string
	ComposePath string
	Service     string
	OldImageID  string
	Pending     bool
	Err         error
}

const (
	selfUpdateComposeLabel  = "watchlet.self-update.compose"
	selfUpdateServiceLabel  = "watchlet.self-update.service"
	selfUpdateOldImageLabel = "watchlet.self-update.old-image"
)

var selfUpdateHelperWait = 30 * time.Second
var processEnviron = os.Environ

type composeConfig struct {
	Services map[string]composeConfigService `json:"services"`
}

type composeConfigService struct {
	Image string `json:"image"`
}

type containerInspect struct {
	ID    string `json:"Id"`
	Name  string `json:"Name"`
	Image string `json:"Image"`
	State struct {
		Status   string `json:"Status"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
	} `json:"State"`
	Config struct {
		Labels map[string]string `json:"Labels"`
	} `json:"Config"`
	Mounts []containerMount `json:"Mounts"`
}

type containerMount struct {
	Source      string `json:"Source"`
	Destination string `json:"Destination"`
}

func New(runner Runner) Client {
	if runner == nil {
		runner = ExecRunner{}
	}
	return Client{runner: runner}
}

type CommandError struct {
	Action      string
	ComposePath string
	Service     string
	Image       string
	Output      string
	Stderr      string
	Err         error
}

func (e *CommandError) Error() string {
	parts := []string{fmt.Sprintf("%s failed", e.Action)}
	if e.ComposePath != "" {
		parts = append(parts, "compose="+e.ComposePath)
	}
	if e.Service != "" {
		parts = append(parts, "service="+e.Service)
	}
	if e.Image != "" {
		parts = append(parts, "image="+e.Image)
	}
	if e.Err != nil {
		parts = append(parts, "error="+e.Err.Error())
	}
	return strings.Join(parts, " ")
}

func (e *CommandError) Unwrap() error {
	return e.Err
}

func (e *CommandError) CommandDiagnostics() (stdout string, stderr string) {
	return e.Output, e.Stderr
}

func (c Client) CurrentService(ctx context.Context) (SelfService, bool, error) {
	inspectTargets, err := currentContainerInspectTargets()
	if err != nil {
		return SelfService{}, false, err
	}

	var output CommandOutput
	var inspected []containerInspect
	for _, target := range inspectTargets {
		output, err = c.runner.Run(ctx, "docker", "container", "inspect", target)
		if err != nil {
			if isNotFoundOutput(output.Combined()) {
				continue
			}
			return SelfService{}, false, commandError("self-identify", "", "", target, output, err)
		}
		if err := json.Unmarshal([]byte(output.Stdout), &inspected); err != nil {
			return SelfService{}, false, err
		}
		if len(inspected) == 0 {
			return SelfService{}, false, errors.New("current container inspect returned no containers")
		}
		break
	}
	if len(inspected) == 0 {
		if !runningInContainer() {
			return SelfService{}, false, nil
		}
		return SelfService{}, false, commandError("self-identify", "", "", strings.Join(inspectTargets, ","), output, errors.New("current container is not inspectable"))
	}

	labels := inspected[0].Config.Labels
	service := labels["com.docker.compose.service"]
	configFiles := labels["com.docker.compose.project.config_files"]
	if service == "" || configFiles == "" {
		if runningInContainer() || hasComposeLabel(labels) {
			return SelfService{}, false, errors.New("current container is missing Docker Compose service identity labels")
		}
		return SelfService{}, false, nil
	}

	composePaths := []string{}
	seenPaths := map[string]bool{}
	for _, configFile := range splitComposeConfigFiles(configFiles) {
		composePaths = appendUnique(composePaths, seenPaths, configFile)
		for _, translated := range translateMountPaths(configFile, inspected[0].Mounts) {
			composePaths = appendUnique(composePaths, seenPaths, translated)
		}
	}
	return SelfService{
		Service:       service,
		ComposePaths:  composePaths,
		ContainerID:   inspected[0].ID,
		ContainerName: strings.TrimPrefix(inspected[0].Name, "/"),
		ImageID:       inspected[0].Image,
	}, true, nil
}

func (c Client) Pull(ctx context.Context, composePath string, service string) error {
	output, err := c.runner.Run(ctx, "docker", composeArgs(composePath, "pull", service)...)
	if err != nil {
		return commandError("pull", composePath, service, "", output, err)
	}
	return nil
}

func (c Client) Recreate(ctx context.Context, composePath string, service string, force bool) error {
	output, err := c.runner.Run(ctx, "docker", recreateArgs(composePath, service, force)...)
	if err != nil {
		return commandError("recreate", composePath, service, "", output, err)
	}
	return nil
}

// HandOffSelfRecreate starts a helper container that runs the Compose recreate
// of Watchlet's own service. Compose stops the old container partway through a
// recreate, which kills any Compose client running inside it, so the recreate
// must run in a container that outlives the current one.
func (c Client) HandOffSelfRecreate(ctx context.Context, self SelfService, composePath string, service string, force bool, oldImageID string) (string, error) {
	if self.ContainerID == "" || self.ContainerName == "" || self.ImageID == "" {
		return "", commandError("self-update-handoff", composePath, service, "", CommandOutput{}, errors.New("current container identity is incomplete"))
	}
	if scheme, ok := nonSocketDockerHost(); ok {
		return "", commandError("self-update-handoff", composePath, service, "", CommandOutput{}, fmt.Errorf("self-update needs the Docker socket mounted into Watchlet; DOCKER_HOST uses %s", scheme))
	}
	helper := selfUpdateHelperName(self)
	args := []string{"run", "-d", "--name", helper, "--network", "none", "--volumes-from", self.ContainerID}
	for _, name := range environmentNames() {
		args = append(args, "-e", name)
	}
	args = append(args,
		"--label", selfUpdateComposeLabel+"="+composePath,
		"--label", selfUpdateServiceLabel+"="+service,
	)
	if oldImageID != "" {
		args = append(args, "--label", selfUpdateOldImageLabel+"="+oldImageID)
	}
	args = append(args, "--entrypoint", "docker", self.ImageID)
	args = append(args, recreateArgs(composePath, service, force)...)

	output, err := c.runner.Run(ctx, "docker", args...)
	if err != nil {
		return "", commandError("self-update-handoff", composePath, service, "", output, err)
	}
	return helper, nil
}

// CollectSelfUpdate reads and removes a self-update helper left by an earlier
// pass for this Watchlet service, if there is one. Helpers are found by their
// labels rather than by name, because the container that started a helper (a
// one-off `docker compose run`, or one with a different container_name) may
// not share the current container's name.
func (c Client) CollectSelfUpdate(ctx context.Context, self SelfService) (SelfUpdateOutcome, bool, error) {
	output, err := c.runner.Run(ctx, "docker", "container", "ls", "-a", "--filter", "label="+selfUpdateServiceLabel+"="+self.Service, "--format", "{{.Names}}")
	if err != nil {
		return SelfUpdateOutcome{}, true, commandError("self-update-collect", "", self.Service, "", output, err)
	}
	for _, name := range strings.Fields(output.Stdout) {
		helper, found, err := c.inspectHelper(ctx, name)
		if err != nil {
			return SelfUpdateOutcome{Helper: name}, true, err
		}
		if found && composePathIn(helper.Config.Labels[selfUpdateComposeLabel], self.ComposePaths) {
			return c.collectHelper(ctx, name, helper)
		}
	}

	reserved := selfUpdateHelperName(self)
	_, found, err := c.inspectHelper(ctx, reserved)
	if err != nil {
		return SelfUpdateOutcome{Helper: reserved}, true, err
	}
	if !found {
		return SelfUpdateOutcome{}, false, nil
	}
	return SelfUpdateOutcome{Helper: reserved}, true, commandError("self-update-collect", "", "", "", CommandOutput{}, fmt.Errorf("container %s exists but is not a self-update helper for this Watchlet service; leaving it untouched", reserved))
}

func (c Client) inspectHelper(ctx context.Context, name string) (containerInspect, bool, error) {
	output, err := c.runner.Run(ctx, "docker", "container", "inspect", name)
	if err != nil {
		if isNotFoundOutput(output.Combined()) {
			return containerInspect{}, false, nil
		}
		return containerInspect{}, false, commandError("self-update-collect", "", "", "", output, err)
	}
	var inspected []containerInspect
	if err := json.Unmarshal([]byte(output.Stdout), &inspected); err != nil || len(inspected) == 0 {
		if err == nil {
			err = errors.New("helper inspect returned no containers")
		}
		return containerInspect{}, false, commandError("self-update-collect", "", "", "", output, err)
	}
	return inspected[0], true, nil
}

func (c Client) collectHelper(ctx context.Context, helper string, inspected containerInspect) (SelfUpdateOutcome, bool, error) {
	labels := inspected.Config.Labels
	outcome := SelfUpdateOutcome{
		Helper:      helper,
		ComposePath: labels[selfUpdateComposeLabel],
		Service:     labels[selfUpdateServiceLabel],
		OldImageID:  labels[selfUpdateOldImageLabel],
	}
	collectError := func(output CommandOutput, err error) (SelfUpdateOutcome, bool, error) {
		return outcome, true, commandError("self-update-collect", outcome.ComposePath, outcome.Service, "", output, err)
	}

	state := inspected.State
	exitCode := state.ExitCode
	switch {
	case state.Running:
		waitCtx, cancel := context.WithTimeout(ctx, selfUpdateHelperWait)
		output, err := c.runner.Run(waitCtx, "docker", "container", "wait", helper)
		deadlineExceeded := errors.Is(waitCtx.Err(), context.DeadlineExceeded)
		cancel()
		if deadlineExceeded {
			outcome.Pending = true
			return outcome, true, nil
		}
		if err != nil {
			return collectError(output, err)
		}
		exitCode, err = strconv.Atoi(strings.TrimSpace(output.Stdout))
		if err != nil {
			return collectError(output, err)
		}
	case state.Status != "exited":
		// A helper that never ran (for example "created" when docker run was
		// interrupted before start) also reports exit code 0.
		outcome.Err = commandError("self-update", outcome.ComposePath, outcome.Service, "", CommandOutput{}, fmt.Errorf("helper %s never ran: status %s", helper, state.Status))
	}

	if outcome.Err == nil && exitCode != 0 {
		exitErr := fmt.Errorf("helper %s exited with code %d", helper, exitCode)
		logs, err := c.runner.Run(ctx, "docker", "container", "logs", helper)
		if err != nil {
			exitErr = fmt.Errorf("helper %s exited with code %d; its output could not be read: %w", helper, exitCode, err)
		}
		outcome.Err = commandError("self-update", outcome.ComposePath, outcome.Service, "", logs, exitErr)
	}
	if outcome.Err != nil {
		outcome.OldImageID = ""
	}

	output, err := c.runner.Run(ctx, "docker", "container", "rm", helper)
	if err != nil {
		return collectError(output, err)
	}
	return outcome, true, nil
}

func composePathIn(path string, paths []string) bool {
	if path == "" {
		return false
	}
	for _, candidate := range paths {
		if filepath.Clean(candidate) == filepath.Clean(path) {
			return true
		}
	}
	return false
}

func selfUpdateHelperName(self SelfService) string {
	return self.ContainerName + "-self-update"
}

// environmentNames lists the variables the helper should inherit by name, so
// Compose interpolation in the helper matches Watchlet's without values
// appearing in the docker run command line.
func environmentNames() []string {
	names := []string{}
	for _, entry := range processEnviron() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "" || name == "HOSTNAME" {
			continue
		}
		names = append(names, name)
	}
	return names
}

// nonSocketDockerHost reports the scheme of DOCKER_HOST when it points
// somewhere other than a unix socket. The helper runs without a network, so it
// can only reach Docker through the socket it inherits from Watchlet.
func nonSocketDockerHost() (string, bool) {
	for _, entry := range processEnviron() {
		name, value, _ := strings.Cut(entry, "=")
		if name != "DOCKER_HOST" || value == "" {
			continue
		}
		scheme, _, found := strings.Cut(value, "://")
		if !found {
			return "an unrecognized address", true
		}
		if scheme == "unix" {
			return "", false
		}
		return scheme + "://", true
	}
	return "", false
}

func recreateArgs(composePath string, service string, force bool) []string {
	args := composeArgs(composePath, "up", "-d", "--no-deps")
	if force {
		args = append(args, "--force-recreate")
	}
	return append(args, service)
}

func (c Client) ImageID(ctx context.Context, composePath string, service string) (string, error) {
	image, err := c.ResolveImage(ctx, composePath, service)
	if err != nil {
		return "", err
	}

	output, err := c.runner.Run(ctx, "docker", "image", "inspect", "--format", "{{.Id}}", image)
	id := strings.TrimSpace(output.Stdout)
	if err != nil {
		if isNotFoundOutput(output.Combined()) {
			return "", nil
		}
		return "", commandError("image-inspect", composePath, service, image, output, err)
	}
	if id == "" {
		return "", commandError("image-inspect", composePath, service, image, output, errors.New("empty image identity"))
	}
	return id, nil
}

func (c Client) ResolveImage(ctx context.Context, composePath string, service string) (string, error) {
	output, err := c.runner.Run(ctx, "docker", composeArgs(composePath, "config", "--format", "json")...)
	if err != nil {
		return "", commandError("image-resolve", composePath, service, "", output, err)
	}

	var resolved composeConfig
	if err := json.Unmarshal([]byte(output.Stdout), &resolved); err != nil {
		return "", commandError("image-resolve", composePath, service, "", output, err)
	}
	resolvedService, ok := resolved.Services[service]
	if !ok {
		return "", commandError("image-resolve", composePath, service, "", output, errors.New("service not found in resolved compose config"))
	}
	if strings.TrimSpace(resolvedService.Image) == "" {
		return "", commandError("image-resolve", composePath, service, "", output, errors.New("resolved service image is empty"))
	}
	return resolvedService.Image, nil
}

func (c Client) RemoveImage(ctx context.Context, composePath string, imageID string) error {
	output, err := c.runner.Run(ctx, "docker", "image", "rm", imageID)
	if err != nil {
		return commandError("cleanup", composePath, "", imageID, output, err)
	}
	return nil
}

func currentContainerInspectTargets() ([]string, error) {
	seen := map[string]bool{}
	targets := []string{}
	for _, target := range containerIDsFromCgroups() {
		if !seen[target] {
			seen[target] = true
			targets = append(targets, target)
		}
	}
	if !runningInContainer() {
		return targets, nil
	}
	hostname, err := os.Hostname()
	if err != nil {
		return nil, err
	}
	if hostname != "" && !seen[hostname] {
		targets = append(targets, hostname)
	}
	return targets, nil
}

func containerIDsFromCgroups() []string {
	ids := []string{}
	seen := map[string]bool{}
	for _, path := range containerCgroupPaths {
		contents, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		for _, id := range containerIDsFromText(string(contents)) {
			if !seen[id] {
				seen[id] = true
				ids = append(ids, id)
			}
		}
	}
	return ids
}

func containerIDsFromText(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '/' || r == ':' || r == '\n' || r == '\r'
	})
	ids := []string{}
	for _, field := range fields {
		field = strings.TrimSuffix(field, ".scope")
		if i := strings.LastIndex(field, "-"); i >= 0 {
			field = field[i+1:]
		}
		if isContainerID(field) {
			ids = append(ids, field)
		}
	}
	return ids
}

func isContainerID(value string) bool {
	if len(value) < 12 || len(value) > 64 {
		return false
	}
	for _, r := range value {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func runningInContainer() bool {
	for _, path := range containerIndicatorPaths {
		if _, err := os.Stat(path); err == nil {
			return true
		}
	}
	for _, path := range containerCgroupPaths {
		contents, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		text := strings.ToLower(string(contents))
		if strings.Contains(text, "docker") || strings.Contains(text, "containerd") || strings.Contains(text, "kubepods") || strings.Contains(text, "libpod") {
			return true
		}
	}
	return false
}

func splitComposeConfigFiles(value string) []string {
	parts := strings.Split(value, ",")
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if path := strings.TrimSpace(part); path != "" {
			paths = append(paths, path)
		}
	}
	return paths
}

func hasComposeLabel(labels map[string]string) bool {
	for key := range labels {
		if strings.HasPrefix(key, "com.docker.compose.") {
			return true
		}
	}
	return false
}

func appendUnique(paths []string, seen map[string]bool, path string) []string {
	if path == "" || seen[path] {
		return paths
	}
	seen[path] = true
	return append(paths, path)
}

func translateMountPaths(path string, mounts []containerMount) []string {
	type match struct {
		source string
		path   string
	}
	matches := []match{}
	for _, mount := range mounts {
		if mount.Source == "" || mount.Destination == "" {
			continue
		}
		rel, err := filepath.Rel(mount.Source, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, "../") {
			continue
		}
		translated := filepath.Join(mount.Destination, rel)
		if translated != path {
			matches = append(matches, match{source: mount.Source, path: translated})
		}
	}
	sort.SliceStable(matches, func(i, j int) bool {
		return len(matches[i].source) > len(matches[j].source)
	})
	paths := make([]string, 0, len(matches))
	seen := map[string]bool{}
	for _, match := range matches {
		paths = appendUnique(paths, seen, match.path)
	}
	return paths
}

func composeArgs(composePath string, args ...string) []string {
	projectDir := filepath.Dir(composePath)
	composeArgs := []string{"compose", "--project-directory", projectDir, "-f", composePath}
	return append(composeArgs, args...)
}

func commandError(action string, composePath string, service string, image string, output CommandOutput, err error) *CommandError {
	return &CommandError{
		Action:      action,
		ComposePath: composePath,
		Service:     service,
		Image:       image,
		Output:      output.Stdout,
		Stderr:      output.Stderr,
		Err:         err,
	}
}

func isNotFoundOutput(output string) bool {
	lower := strings.ToLower(output)
	return strings.Contains(lower, "no such image") || strings.Contains(lower, "no such container") || strings.Contains(lower, "not found")
}
