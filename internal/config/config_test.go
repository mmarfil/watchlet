package config

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaultsIntervalAndRequiresCompose(t *testing.T) {
	_, err := Load(nil, emptyEnv, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "at least one compose") {
		t.Fatalf("expected missing compose error, got %v", err)
	}
}

func TestLoadUsesDefaultInterval(t *testing.T) {
	workdir := t.TempDir()
	cfg, err := Load([]string{"--compose", "compose.yml"}, emptyEnv, workdir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Interval != DefaultInterval {
		t.Fatalf("Interval = %v, want %v", cfg.Interval, DefaultInterval)
	}
	want := []string{filepath.Join(workdir, "compose.yml")}
	if !reflect.DeepEqual(cfg.ComposePaths, want) {
		t.Fatalf("ComposePaths = %#v, want %#v", cfg.ComposePaths, want)
	}
}

func TestLoadRepeatedComposeFlagsOverrideEnv(t *testing.T) {
	workdir := t.TempDir()
	cfg, err := Load(
		[]string{"--compose", "one.yml", "--compose", "two.yml"},
		env(map[string]string{EnvCompose: "env.yml"}),
		workdir,
	)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := []string{filepath.Join(workdir, "one.yml"), filepath.Join(workdir, "two.yml")}
	if !reflect.DeepEqual(cfg.ComposePaths, want) {
		t.Fatalf("ComposePaths = %#v, want %#v", cfg.ComposePaths, want)
	}
}

func TestLoadComposeEnvCommaSeparatedList(t *testing.T) {
	workdir := t.TempDir()
	cfg, err := Load(nil, env(map[string]string{EnvCompose: "one.yml, two.yml"}), workdir)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	want := []string{filepath.Join(workdir, "one.yml"), filepath.Join(workdir, "two.yml")}
	if !reflect.DeepEqual(cfg.ComposePaths, want) {
		t.Fatalf("ComposePaths = %#v, want %#v", cfg.ComposePaths, want)
	}
}

func TestLoadIntervalFlagOverridesEnv(t *testing.T) {
	cfg, err := Load(
		[]string{"--compose", "compose.yml", "--interval", "30m"},
		env(map[string]string{EnvInterval: "1h"}),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Interval != 30*time.Minute {
		t.Fatalf("Interval = %v, want 30m", cfg.Interval)
	}
}

func TestLoadIntervalFromEnv(t *testing.T) {
	cfg, err := Load(
		[]string{"--compose", "compose.yml"},
		env(map[string]string{EnvInterval: "1h"}),
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if cfg.Interval != time.Hour {
		t.Fatalf("Interval = %v, want 1h", cfg.Interval)
	}
}

func TestLoadRejectsInvalidInterval(t *testing.T) {
	_, err := Load([]string{"--compose", "compose.yml", "--interval", "soon"}, emptyEnv, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "invalid --interval duration") {
		t.Fatalf("expected invalid interval error, got %v", err)
	}
}

func TestLoadRejectsEmptyComposePath(t *testing.T) {
	_, err := Load([]string{"--compose", ""}, emptyEnv, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "must not be empty") {
		t.Fatalf("expected empty compose path error, got %v", err)
	}
}

func TestLoadParsesOnceAndForce(t *testing.T) {
	cfg, err := Load([]string{"--compose", "compose.yml", "--once", "--force"}, emptyEnv, t.TempDir())
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}
	if !cfg.Once || !cfg.Force {
		t.Fatalf("Once/Force = %v/%v, want true/true", cfg.Once, cfg.Force)
	}
}

func TestLoadRejectsForceWithoutOnce(t *testing.T) {
	_, err := Load([]string{"--compose", "compose.yml", "--force"}, emptyEnv, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "--force requires --once") {
		t.Fatalf("expected force without once error, got %v", err)
	}
}

func emptyEnv(string) string { return "" }

func env(values map[string]string) func(string) string {
	return func(key string) string {
		return values[key]
	}
}
