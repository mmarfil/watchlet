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
	"strings"
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
	Service      string
	ComposePaths []string
}

type composeConfig struct {
	Services map[string]composeConfigService `json:"services"`
}

type composeConfigService struct {
	Image string `json:"image"`
}

type containerInspect struct {
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
	return SelfService{Service: service, ComposePaths: composePaths}, true, nil
}

func (c Client) Pull(ctx context.Context, composePath string, service string) error {
	output, err := c.runner.Run(ctx, "docker", composeArgs(composePath, "pull", service)...)
	if err != nil {
		return commandError("pull", composePath, service, "", output, err)
	}
	return nil
}

func (c Client) Recreate(ctx context.Context, composePath string, service string, force bool) error {
	args := composeArgs(composePath, "up", "-d", "--no-deps")
	if force {
		args = append(args, "--force-recreate")
	}
	args = append(args, service)

	output, err := c.runner.Run(ctx, "docker", args...)
	if err != nil {
		return commandError("recreate", composePath, service, "", output, err)
	}
	return nil
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
