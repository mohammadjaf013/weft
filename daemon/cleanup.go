package daemon

import (
	"context"
	"log"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// cleaner subscribes to job-completion events and, for jobs that asked for it,
// removes the source input file from disk once the job finished uploading.
// It also deletes the per-task work directories so temp media never piles up.
type cleaner struct {
	store    *sqlite.Store
	bus      core.EventBus
	workRoot string
}

func (c *cleaner) Run(ctx context.Context) error {
	sub := c.bus.SubscribeAll()
	for {
		select {
		case <-ctx.Done():
			return nil
		case e, ok := <-sub:
			if !ok {
				return nil
			}
			if e.Kind != core.EvtJobFinished {
				continue
			}
			c.cleanupJob(ctx, e.JobID)
		}
	}
}

func (c *cleaner) cleanupJob(ctx context.Context, jobID core.JobID) {
	job, err := c.store.LoadJob(ctx, jobID)
	if err != nil {
		log.Printf("cleanup: load job %s: %v", jobID, err)
		return
	}

	// Remove the source input file when the job asked for it.
	if job.DeleteSource {
		src := sourcePath(job.InputRef)
		if src != "" {
			if fi, err := os.Stat(src); err == nil && !fi.IsDir() {
				if err := os.Remove(src); err != nil {
					log.Printf("cleanup: delete source %s (job %s): %v", src, jobID, err)
				} else {
					log.Printf("cleanup: deleted source %s (job %s)", src, jobID)
				}
			}
		}
	}

	// Remove per-task temp work dirs for the whole job.
	tasks, err := c.store.ListTasks(ctx, jobID)
	if err != nil {
		log.Printf("cleanup: list tasks %s: %v", jobID, err)
		return
	}
	for _, t := range tasks {
		dir := filepath.Join(c.workRoot, "work", string(t.ID))
		if err := os.RemoveAll(dir); err != nil {
			log.Printf("cleanup: remove %s: %v", dir, err)
		}
	}
	// Drop the now-empty work container if nothing else runs there.
	os.Remove(filepath.Join(c.workRoot, "work"))
}

// sourcePath extracts a local filesystem path from an input ref, or "" when the
// ref points somewhere remote (never touch those).
func sourcePath(ref string) string {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	for _, scheme := range []string{"s3://", "ssh://", "http://", "https://"} {
		if strings.HasPrefix(ref, scheme) {
			return ""
		}
	}
	ref = strings.TrimPrefix(ref, "local:")
	if ref == "" {
		return ""
	}
	// Accept both unix-style (/var/...) and native absolute paths (C:\...).
	// Remote refs were already rejected above.
	if !path.IsAbs(ref) && !filepath.IsAbs(ref) {
		return ""
	}
	return filepath.FromSlash(path.Clean(filepath.ToSlash(ref)))
}

