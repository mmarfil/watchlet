package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"watchlet/internal/config"
	"watchlet/internal/interval"
	"watchlet/internal/updatepass"
	"watchlet/internal/watchlog"
)

func main() {
	workdir, err := os.Getwd()
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchlet: determine working directory: %v\n", err)
		os.Exit(1)
	}

	cfg, err := config.Load(os.Args[1:], os.Getenv, workdir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchlet: invalid config: %v\n", err)
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := updatepass.New(os.Stdout, nil)
	if cfg.Once {
		if _, err := runner.Run(ctx, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "watchlet: update pass failed: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if err := interval.Run(ctx, cfg, runner, watchlog.New(os.Stdout), interval.Sleep); err != nil {
		fmt.Fprintf(os.Stderr, "watchlet: interval mode failed: %v\n", err)
		os.Exit(1)
	}
}
