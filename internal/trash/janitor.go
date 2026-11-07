package trash

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"
)

// Trash folders are named after the UTC day their files were deleted.
const dayLayout = "2006-01-02"

// Empty removes trash days older than keep. It returns how many day folders
// it removed.
func Empty(dir string, keep time.Duration, now time.Time) (int, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	cutoff := now.UTC().Add(-keep)
	removed := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		day, err := time.Parse(dayLayout, e.Name())
		if err != nil {
			continue // not ours, leave it alone
		}
		// A day's files can be up to 24h younger than the folder name says.
		if day.Add(24 * time.Hour).After(cutoff) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(dir, e.Name())); err != nil {
			return removed, err
		}
		removed++
	}
	return removed, nil
}

// RunJanitor empties old trash once at start and then every hour until ctx
// is cancelled.
func RunJanitor(ctx context.Context, dir string, keep time.Duration) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		n, err := Empty(dir, keep, time.Now())
		if err != nil {
			slog.Error("emptying trash", "err", err)
		} else if n > 0 {
			slog.Info("emptied trash", "days", n)
		}
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
