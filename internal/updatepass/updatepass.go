package updatepass

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"watchlet/internal/composefile"
	"watchlet/internal/config"
	"watchlet/internal/dockercompose"
	"watchlet/internal/watchlog"
)

type Parser interface {
	Load(path string) (composefile.File, error)
}

type Commander interface {
	CurrentService(ctx context.Context) (dockercompose.SelfService, bool, error)
	ImageID(ctx context.Context, composePath string, service string) (string, error)
	Pull(ctx context.Context, composePath string, service string) error
	Recreate(ctx context.Context, composePath string, service string, force bool) error
	RemoveImage(ctx context.Context, composePath string, imageID string) error
}

type FileParser struct{}

func (FileParser) Load(path string) (composefile.File, error) {
	return composefile.Load(path)
}

type Runner struct {
	Parser    Parser
	Commander Commander
	Logger    watchlog.Logger
}

type Result struct {
	ComposeFiles int
	Selected     int
	Updated      int
	Current      int
	Forced       int
	Skipped      int
	Failed       int
}

type composeRun struct {
	composePath     string
	result          Result
	deferredUpdates []serviceState
	cleanupTargets  []cleanupTarget
}

type serviceState struct {
	service    composefile.Service
	before     string
	selfUpdate bool
	failed     bool
}

type cleanupTarget struct {
	service    composefile.Service
	oldImageID string
}

type cleanupTracker struct {
	succeeded map[string]bool
}

func New(output io.Writer, commandRunner dockercompose.Runner) Runner {
	return Runner{
		Parser:    FileParser{},
		Commander: dockercompose.New(commandRunner),
		Logger:    watchlog.New(output),
	}
}

func (r Runner) Run(ctx context.Context, cfg config.Config) (Result, error) {
	if r.Parser == nil {
		r.Parser = FileParser{}
	}
	if r.Commander == nil {
		r.Commander = dockercompose.New(nil)
	}

	result := Result{ComposeFiles: len(cfg.ComposePaths)}
	deferredRuns := []composeRun{}
	cleanupTargets := []cleanupTarget{}
	cleanupTracker := cleanupTracker{succeeded: map[string]bool{}}
	r.Logger.PassStart(cfg.Interval.String(), cfg.Once, cfg.Force, len(cfg.ComposePaths))
	for _, composePath := range cfg.ComposePaths {
		r.Logger.ComposeConfigured(composePath)
	}

	selfService, hasSelfService, err := r.Commander.CurrentService(ctx)
	if err != nil {
		result.Failed++
		r.Logger.Failure("self-identify", "", "", err)
		r.Logger.PassSummary("failed", result.ComposeFiles, result.Selected, result.Updated, result.Current, result.Forced, result.Skipped, result.Failed)
		return result, fmt.Errorf("update pass failed: self-identify: %w", err)
	}

	for _, composePath := range cfg.ComposePaths {
		fileRun := r.runCompose(ctx, cfg, composePath, selfService, hasSelfService)
		result.add(fileRun.result)
		cleanupTargets = append(cleanupTargets, fileRun.cleanupTargets...)
		if len(fileRun.deferredUpdates) > 0 {
			deferredRuns = append(deferredRuns, fileRun)
		}
	}

	preSelfFailures := r.cleanupTargets(ctx, cleanupTargets, cleanupTracker)
	for _, deferredRun := range deferredRuns {
		selfResult, selfCleanupTargets := r.runDeferredSelfUpdates(ctx, cfg, deferredRun.deferredUpdates)
		result.add(selfResult)
		deferredRun.result.add(selfResult)
		r.cleanupTargets(ctx, append(selfCleanupTargets, preSelfFailures...), cleanupTracker)
		r.logComposeResult(deferredRun.composePath, deferredRun.result)
	}

	status := "ok"
	if result.Failed > 0 {
		status = "failed"
		err = fmt.Errorf("update pass failed: failed=%d", result.Failed)
	}
	r.Logger.PassSummary(status, result.ComposeFiles, result.Selected, result.Updated, result.Current, result.Forced, result.Skipped, result.Failed)
	return result, err
}

func (r Runner) runCompose(ctx context.Context, cfg config.Config, composePath string, selfService dockercompose.SelfService, hasSelfService bool) composeRun {
	var result Result

	file, err := r.Parser.Load(composePath)
	if err != nil {
		result.Failed++
		r.Logger.Failure("compose-parse", composePath, "", err)
		r.Logger.ComposeResult(composePath, "failed", 0, 0, 0, 0, 0, 1)
		return composeRun{composePath: composePath, result: result}
	}

	for _, skipped := range file.Skipped {
		result.Skipped++
		r.Logger.ServiceSkipped(skipped.ComposePath, skipped.Name, string(skipped.Reason))
	}
	for _, invalid := range file.Invalid {
		result.Failed++
		r.Logger.ServiceSkipped(invalid.ComposePath, invalid.Name, string(composefile.SkipInvalidService))
		r.Logger.Failure("compose-parse", invalid.ComposePath, invalid.Name, errors.New(invalid.Reason))
	}

	states := make([]serviceState, 0, len(file.Services))
	for _, service := range file.Services {
		result.Selected++
		r.Logger.ServiceSelected(service.ComposePath, service.Name, service.Image)
		states = append(states, serviceState{
			service:    service,
			selfUpdate: hasSelfService && isCurrentWatchletService(service, selfService),
		})
	}

	r.recordBeforeIdentities(ctx, states, &result)

	activeStates, deferredUpdates := splitSelfUpdates(states)
	r.pullServices(ctx, activeStates, &result)
	cleanupTargets := r.inspectAndRecreate(ctx, cfg, activeStates, &result)

	if len(deferredUpdates) > 0 {
		r.logComposeResultWithStatus(composePath, "self-update-deferred", result)
	} else {
		r.logComposeResult(composePath, result)
	}
	return composeRun{composePath: composePath, result: result, deferredUpdates: deferredUpdates, cleanupTargets: cleanupTargets}
}

func (r Runner) runDeferredSelfUpdates(ctx context.Context, cfg config.Config, states []serviceState) (Result, []cleanupTarget) {
	for _, state := range states {
		r.Logger.ActionResult("self-update", state.service.ComposePath, state.service.Name, "deferred", "self-update-last", nil)
	}

	var result Result
	r.pullServices(ctx, states, &result)
	cleanupTargets := r.inspectAndRecreate(ctx, cfg, states, &result)
	return result, cleanupTargets
}

func (r Runner) recordBeforeIdentities(ctx context.Context, states []serviceState, result *Result) {
	for i := range states {
		if states[i].failed {
			continue
		}
		service := states[i].service
		before, err := r.Commander.ImageID(ctx, service.ComposePath, service.Name)
		if err != nil {
			states[i].failed = true
			result.Failed++
			r.Logger.ActionResult("image-inspect", service.ComposePath, service.Name, "failed", "image-inspect-failed", err)
			continue
		}
		states[i].before = before
	}
}

func (r Runner) pullServices(ctx context.Context, states []serviceState, result *Result) {
	for i := range states {
		if states[i].failed {
			continue
		}
		service := states[i].service
		if err := r.Commander.Pull(ctx, service.ComposePath, service.Name); err != nil {
			states[i].failed = true
			result.Failed++
			r.Logger.ActionResult("pull", service.ComposePath, service.Name, "failed", "pull-failed", err)
			continue
		}
		r.Logger.ActionResult("pull", service.ComposePath, service.Name, "ok", "", nil)
	}
}

func (r Runner) inspectAndRecreate(ctx context.Context, cfg config.Config, states []serviceState, result *Result) []cleanupTarget {
	cleanupTargets := []cleanupTarget{}
	for i := range states {
		if states[i].failed {
			continue
		}
		service := states[i].service
		after, err := r.Commander.ImageID(ctx, service.ComposePath, service.Name)
		if err != nil {
			states[i].failed = true
			result.Failed++
			r.Logger.ActionResult("image-inspect", service.ComposePath, service.Name, "failed", "image-inspect-failed", err)
			continue
		}
		if after == "" {
			states[i].failed = true
			result.Failed++
			r.Logger.ActionResult("image-inspect", service.ComposePath, service.Name, "failed", "image-inspect-failed", errors.New("image identity unavailable after pull"))
			continue
		}
		changed := states[i].before != after
		if !changed && !cfg.Force {
			result.Current++
			r.Logger.ActionResult("recreate", service.ComposePath, service.Name, "skipped", "current", nil)
			continue
		}

		if states[i].selfUpdate && (changed || cfg.Force) {
			r.Logger.ActionResult("self-update", service.ComposePath, service.Name, "starting", "self-update-recreate", nil)
		}
		if err := r.Commander.Recreate(ctx, service.ComposePath, service.Name, cfg.Force); err != nil {
			states[i].failed = true
			result.Failed++
			r.Logger.ActionResult("recreate", service.ComposePath, service.Name, "failed", "recreate-failed", err)
			continue
		}
		if cfg.Force && !changed {
			result.Forced++
			r.Logger.ActionResult("recreate", service.ComposePath, service.Name, "ok", "forced", nil)
			continue
		}

		result.Updated++
		r.Logger.ActionResult("recreate", service.ComposePath, service.Name, "ok", "changed", nil)
		if states[i].before != "" {
			cleanupTargets = append(cleanupTargets, cleanupTarget{service: service, oldImageID: states[i].before})
		}
	}
	return cleanupTargets
}

func (r Runner) cleanupTargets(ctx context.Context, targets []cleanupTarget, tracker cleanupTracker) []cleanupTarget {
	attempted := map[string]bool{}
	failed := []cleanupTarget{}
	for _, target := range targets {
		key := cleanupKey(target)
		if tracker.succeeded[key] || attempted[key] {
			continue
		}
		attempted[key] = true
		if r.cleanup(ctx, target.service, target.oldImageID) {
			tracker.succeeded[key] = true
			continue
		}
		failed = append(failed, target)
	}
	return failed
}

func cleanupKey(target cleanupTarget) string {
	return target.service.ComposePath + "\x00" + target.oldImageID
}

func (r Runner) cleanup(ctx context.Context, service composefile.Service, oldImageID string) bool {
	if oldImageID == "" {
		return false
	}
	if err := r.Commander.RemoveImage(ctx, service.ComposePath, oldImageID); err != nil {
		r.Logger.CleanupResult(service.ComposePath, service.Name, oldImageID, "skipped", "cleanup-failed", err)
		return false
	}
	r.Logger.CleanupResult(service.ComposePath, service.Name, oldImageID, "ok", "", nil)
	return true
}

func (r Runner) logComposeResult(composePath string, result Result) {
	status := "ok"
	if result.Failed > 0 {
		status = "failed"
	}
	r.logComposeResultWithStatus(composePath, status, result)
}

func (r Runner) logComposeResultWithStatus(composePath string, status string, result Result) {
	r.Logger.ComposeResult(composePath, status, result.Selected, result.Skipped, result.Updated, result.Current, result.Forced, result.Failed)
}

func splitSelfUpdates(states []serviceState) ([]serviceState, []serviceState) {
	active := make([]serviceState, 0, len(states))
	deferred := []serviceState{}
	for _, state := range states {
		if state.selfUpdate {
			deferred = append(deferred, state)
			continue
		}
		active = append(active, state)
	}
	return active, deferred
}

func isCurrentWatchletService(service composefile.Service, self dockercompose.SelfService) bool {
	if service.Name != self.Service {
		return false
	}
	servicePath := filepath.Clean(service.ComposePath)
	for _, selfPath := range self.ComposePaths {
		if servicePath == filepath.Clean(selfPath) {
			return true
		}
	}
	return false
}

func (r *Result) add(other Result) {
	r.Selected += other.Selected
	r.Updated += other.Updated
	r.Current += other.Current
	r.Forced += other.Forced
	r.Skipped += other.Skipped
	r.Failed += other.Failed
}
