package interval

import (
	"context"
	"errors"
	"time"

	"watchlet/internal/config"
	"watchlet/internal/updatepass"
	"watchlet/internal/watchlog"
)

type PassRunner interface {
	Run(ctx context.Context, cfg config.Config) (updatepass.Result, error)
}

type SleepFunc func(ctx context.Context, duration time.Duration) error

func Sleep(ctx context.Context, duration time.Duration) error {
	timer := time.NewTimer(duration)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func Run(ctx context.Context, cfg config.Config, runner PassRunner, logger watchlog.Logger, sleep SleepFunc) error {
	if sleep == nil {
		sleep = Sleep
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil
		}

		_, err := runner.Run(ctx, cfg)
		if err != nil {
			logger.PassEnd("failed", err)
		} else {
			logger.PassEnd("ok", nil)
		}

		logger.NextSleep(cfg.Interval.String())
		if err := sleep(ctx, cfg.Interval); err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil
			}
			return err
		}
	}
}
