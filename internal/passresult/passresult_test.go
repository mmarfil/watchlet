package passresult

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"watchlet/internal/updatepass"
)

func TestWriteReplacesResult(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "last-pass.json")
	started := time.Date(2026, 8, 18, 10, 0, 0, 0, time.FixedZone("local", 3600))
	result := updatepass.Result{ComposeFiles: 2, Selected: 10, Updated: 1, Current: 8, Skipped: 4, Failed: 1}

	if err := Write(path, started, started.Add(time.Minute), result, errors.New("failed")); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile returned error: %v", err)
	}
	var got Record
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal returned error: %v", err)
	}
	if got.Status != "failed" || got.Failed != 1 || got.StartedAt != "2026-08-18T09:00:00Z" {
		t.Fatalf("record = %#v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat returned error: %v", err)
	}
	if info.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %o, want 644", info.Mode().Perm())
	}
}

func TestWriteDoesNothingWithoutPath(t *testing.T) {
	if err := Write("", time.Now(), time.Now(), updatepass.Result{}, nil); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
}
