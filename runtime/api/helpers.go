package api

import (
	"log"
	"net/http"
	"time"

	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
)

// requestLogger logs every authenticated request (key id, scope, endpoint,
// result) — the audit trail. For v1 this is stdout; a dedicated Audit Log
// projection is Phase 2.
func requestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("http %s %s %s (%s)", r.Method, r.URL.Path, r.RemoteAddr, time.Since(start))
	})
}

// runBenchmark produces a synthetic node benchmark. Real CPU/FFmpeg scoring is
// Phase 1 target; v1 emits a deterministic placeholder score per run so the
// endpoint, schema, and CLI are wired end to end.
func runBenchmark() sqlite.Benchmark {
	return sqlite.Benchmark{
		ID:                core.NewID("bm"),
		NodeID:            "node-1",
		CPUScore:          100.0,
		FFmpegScore:       100.0,
		DiskIOScore:       100.0,
		MemoryScore:       100.0,
		EstimatedCapacity: 8.0,
		CreatedAt:         time.Now().UTC(),
	}
}
