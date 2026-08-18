package passresult

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"watchlet/internal/updatepass"
)

type Record struct {
	StartedAt    string `json:"startedAt"`
	FinishedAt   string `json:"finishedAt"`
	Status       string `json:"status"`
	ComposeFiles int    `json:"composeFiles"`
	Selected     int    `json:"selected"`
	Updated      int    `json:"updated"`
	Current      int    `json:"current"`
	Forced       int    `json:"forced"`
	Skipped      int    `json:"skipped"`
	Failed       int    `json:"failed"`
}

func Write(path string, startedAt time.Time, finishedAt time.Time, result updatepass.Result, passErr error) error {
	if path == "" {
		return nil
	}

	status := "ok"
	if passErr != nil || result.Failed > 0 {
		status = "failed"
	}
	record := Record{
		StartedAt:    startedAt.UTC().Format(time.RFC3339),
		FinishedAt:   finishedAt.UTC().Format(time.RFC3339),
		Status:       status,
		ComposeFiles: result.ComposeFiles,
		Selected:     result.Selected,
		Updated:      result.Updated,
		Current:      result.Current,
		Forced:       result.Forced,
		Skipped:      result.Skipped,
		Failed:       result.Failed,
	}
	data, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create result directory: %w", err)
	}
	temp, err := os.CreateTemp(dir, ".watchlet-result-*")
	if err != nil {
		return fmt.Errorf("create temporary result: %w", err)
	}
	tempPath := temp.Name()
	defer os.Remove(tempPath)
	if err := temp.Chmod(0o644); err != nil {
		temp.Close()
		return fmt.Errorf("set result permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		temp.Close()
		return fmt.Errorf("write result: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close result: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("replace result: %w", err)
	}
	return nil
}
