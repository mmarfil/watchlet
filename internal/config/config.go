package config

import (
	"errors"
	"flag"
	"fmt"
	"path/filepath"
	"strings"
	"time"
)

const (
	DefaultInterval = 24 * time.Hour

	EnvCompose  = "WATCHLET_COMPOSE"
	EnvInterval = "WATCHLET_INTERVAL"
)

type Config struct {
	ComposePaths []string
	Interval     time.Duration
	Once         bool
	Force        bool
}

type composeFlags []string

func (c *composeFlags) String() string {
	return strings.Join(*c, ",")
}

func (c *composeFlags) Set(value string) error {
	*c = append(*c, value)
	return nil
}

func Load(args []string, getenv func(string) string, workdir string) (Config, error) {
	if getenv == nil {
		getenv = func(string) string { return "" }
	}

	var compose composeFlags
	var intervalValue string
	var cfg Config

	fs := flag.NewFlagSet("watchlet", flag.ContinueOnError)
	fs.SetOutput(&strings.Builder{})
	fs.Var(&compose, "compose", "Docker Compose file path; may be repeated")
	fs.StringVar(&intervalValue, "interval", "", "update interval as a Go duration")
	fs.BoolVar(&cfg.Once, "once", false, "run one update pass and exit")
	fs.BoolVar(&cfg.Force, "force", false, "recreate selected image services after pulling")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	flagWasSet := map[string]bool{}
	fs.Visit(func(f *flag.Flag) {
		flagWasSet[f.Name] = true
	})

	composeValues := []string(compose)
	if !flagWasSet["compose"] {
		composeValues = splitComposeEnv(getenv(EnvCompose))
	}

	paths, err := ResolveComposePaths(composeValues, workdir)
	if err != nil {
		return Config{}, err
	}
	cfg.ComposePaths = paths

	if flagWasSet["interval"] {
		cfg.Interval, err = parseInterval(intervalValue, "--interval")
	} else if envInterval := strings.TrimSpace(getenv(EnvInterval)); envInterval != "" {
		cfg.Interval, err = parseInterval(envInterval, EnvInterval)
	} else {
		cfg.Interval = DefaultInterval
	}
	if err != nil {
		return Config{}, err
	}

	if cfg.Force && !cfg.Once {
		return Config{}, errors.New("--force requires --once")
	}

	return cfg, nil
}

func splitComposeEnv(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return strings.Split(value, ",")
}

func ResolveComposePaths(values []string, workdir string) ([]string, error) {
	if len(values) == 0 {
		return nil, errors.New("at least one compose file path is required; set --compose or WATCHLET_COMPOSE")
	}
	if workdir == "" {
		workdir = "."
	}

	paths := make([]string, 0, len(values))
	for _, value := range values {
		path := strings.TrimSpace(value)
		if path == "" {
			return nil, errors.New("compose file paths must not be empty")
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(workdir, path)
		}
		abs, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve compose path %q: %w", value, err)
		}
		paths = append(paths, filepath.Clean(abs))
	}
	return paths, nil
}

func parseInterval(value string, source string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s must not be empty", source)
	}
	interval, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid %s duration %q: %w", source, value, err)
	}
	if interval <= 0 {
		return 0, fmt.Errorf("%s must be greater than zero", source)
	}
	return interval, nil
}
