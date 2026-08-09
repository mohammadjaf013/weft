// Package api implements the REST API (chi) and API-key auth. The CLI is a
// thin client over exactly these handlers — nothing is CLI-only.
package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/rebuild"
	"github.com/mohammadjaf013/weft/profiles"
	"github.com/mohammadjaf013/weft/runtime/cron"
	"github.com/mohammadjaf013/weft/runtime/metrics"
	"github.com/mohammadjaf013/weft/runtime/registry"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
	"github.com/mohammadjaf013/weft/runtime/sysinfo"
)

type Server struct {
	store    *sqlite.Store
	bus      core.EventBus
	sched    core.DAGScheduler
	sm       core.StateMachine
	registry *registry.Registry
	metrics  *metrics.Metrics
	cfg      *cfg.Config
	cfgPath  string
	keys     *KeyManager
	worker   WorkerHandle
	scaler   WorkerScaler
	stbuild  StorageBuilder
	cronSched *cron.Scheduler
}

// StorageBuilder constructs the destination storage for a registered server id
// (or destination 0 = default local). Injected by the daemon so the api
// package stays free of storage-plugin imports.
type StorageBuilder func(ctx context.Context, destinationID int, destPath string) (core.Storage, error)

// WorkerHandle lets the API report current worker state to /workers.
type WorkerHandle interface {
	Snapshot() []WorkerState
}

// WorkerScaler lets the API scale the worker pool at runtime. Implemented by
// the daemon's worker pool; nil means the endpoint is disabled.
type WorkerScaler interface {
	Scale(ctx context.Context, target int) error
}

type WorkerState struct {
	ID            string    `json:"id"`
	Status        string    `json:"status"`
	CurrentTaskID string    `json:"current_task_id,omitempty"`
	LastHeartbeat time.Time `json:"last_heartbeat_at"`
}

type Options struct {
	Store    *sqlite.Store
	Bus      core.EventBus
	Sched    core.DAGScheduler
	SM       core.StateMachine
	Registry *registry.Registry
	Metrics  *metrics.Metrics
	Config   *cfg.Config
	// ConfigPath is the weft.yaml file this server was started from. When set,
	// POST /config/import writes back to it (applied on next restart); empty
	// disables config import (returns 501).
	ConfigPath string
	Keys       *KeyManager
	Worker   WorkerHandle
	// WorkerScaler, when non-nil, enables POST /workers/scale (pool sizing).
	WorkerScaler WorkerScaler
	// Storage builds destination storage for a server id / subdir (nil ⇒ no
	// storage-admin endpoints such as rebuild-master).
	Storage StorageBuilder
	// CronScheduler backs GET /cron and POST /cron/{job}/run. nil ⇒ those
	// endpoints report an empty job list / 501.
	CronScheduler *cron.Scheduler
}

func NewServer(o Options) *Server {
	return &Server{
		store:    o.Store,
		bus:      o.Bus,
		sched:    o.Sched,
		sm:       o.SM,
		registry: o.Registry,
		metrics:  o.Metrics,
		cfg:      o.Config,
		cfgPath:  o.ConfigPath,
		keys:     o.Keys,
		worker:   o.Worker,
		scaler:   o.WorkerScaler,
		stbuild:  o.Storage,
		cronSched: o.CronScheduler,
	}
}

// SetCronScheduler wires the cron scheduler in after construction. Exists
// because daemon.Open builds the Server before the scheduler (the
// scheduler's "benchmark" job calls Server.RunBenchmark, so the Server must
// already exist) — a plain Options field can't express that ordering.
func (s *Server) SetCronScheduler(c *cron.Scheduler) {
	s.cronSched = c
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(requestLogger)

	r.Get("/", s.handleRoot)
	r.Get("/health", s.handleHealth)

	// authenticated + scoped routes
	r.Group(func(pr chi.Router) {
		pr.Use(s.authMiddleware)
		pr.Get("/jobs", s.handleListJobs)                 // jobs:read
		pr.Post("/jobs", s.handleCreateJob)               // jobs:write
		pr.Get("/jobs/{id}", s.handleGetJob)              // jobs:read
		pr.Get("/jobs/{id}/events", s.handleJobEvents)    // jobs:read
		pr.Get("/jobs/{id}/tasks/{taskID}/log", s.handleTaskLog) // jobs:read
		pr.Get("/jobs/{id}/assets/{name}", s.handleGetAsset) // jobs:read
		pr.Delete("/jobs/{id}", s.handleDeleteJob)        // jobs:write
		pr.Patch("/jobs/{id}/priority", s.handleUpdateJobPriority) // jobs:write
		pr.Post("/jobs/{id}/{action}", s.handleJobAction) // jobs:write

		pr.Get("/queue", s.handleQueue)     // queue:read
		pr.Get("/workers", s.handleWorkers) // workers:read
		pr.Post("/workers/scale", s.handleScaleWorkers) // workers:write

		pr.Get("/storage/servers", s.handleListServers) // storage:manage
		pr.Post("/storage/servers", s.handleAddServer)  // storage:manage
		pr.Post("/storage/rebuild-master", s.handleRebuildMaster) // storage:manage

		pr.Get("/webhooks", s.handleListWebhooks)                     // webhooks:manage
		pr.Post("/webhooks", s.handleCreateWebhook)                   // webhooks:manage
		pr.Delete("/webhooks/{id}", s.handleDeleteWebhook)            // webhooks:manage
		pr.Post("/webhooks/{event_id}/replay", s.handleReplayWebhook) // webhooks:manage

		pr.Get("/keys", s.handleListKeys)          // keys:manage
		pr.Post("/keys", s.handleCreateKey)        // keys:manage
		pr.Delete("/keys/{id}", s.handleDeleteKey) // keys:manage

		pr.Get("/profiles", s.handleListProfiles) // profiles:read
		pr.Get("/plugins", s.handleListPlugins)   // plugins:read

		pr.Get("/config/export", s.handleConfigExport) // config:manage
		pr.Post("/config/import", s.handleConfigImport) // config:manage

		pr.Get("/cron", s.handleListCron)          // cron:manage
		pr.Post("/cron/{job}/run", s.handleRunCron) // cron:manage

		pr.Post("/benchmark", s.handleRunBenchmark) // metrics:read
		pr.Get("/benchmark", s.handleGetBenchmark)  // metrics:read
		pr.Get("/metrics", s.handleMetrics)         // metrics:read
		pr.Get("/system", s.handleSystem)           // metrics:read
	})

	return r
}

// --- error shape (Error Handling Taxonomy: Request/validation errors) ---

type apiError struct {
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	var e apiError
	e.Error.Code = code
	e.Error.Message = msg
	writeJSON(w, status, e)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// handleRoot reports service identity + capabilities: version, profiles,
// plugins, and the public surface. Useful as a handshake and for debugging.
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	profiles := profiles.List()
	pnames := make([]string, 0, len(profiles))
	for _, p := range profiles {
		pnames = append(pnames, p.Name)
	}
	plugs := s.registry.List()
	plugsOut := make([]map[string]any, 0, len(plugs))
	for _, p := range plugs {
		c := p.Capabilities()
		plugsOut = append(plugsOut, map[string]any{
			"name":  c.Name,
			"kinds": c.SupportedKinds,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"service": "weft",
		"version": core.Version,
		"status":  "ok",
		"profiles": pnames,
		"plugins": plugsOut,
		"endpoints": []string{
			"/health", "/jobs", "/queue", "/workers",
			"/profiles", "/plugins", "/metrics", "/webhooks", "/keys",
			"/storage/servers", "/benchmark",
		},
	})
}

// --- auth ---

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.cfg.Security.APIKeys {
			next.ServeHTTP(w, r)
			return
		}
		auth := r.Header.Get("Authorization")
		if !strings.HasPrefix(auth, "Bearer ") {
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		token := strings.TrimPrefix(auth, "Bearer ")
		key, err := s.keys.Verify(token)
		if err != nil {
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid api key")
			return
		}
		// scope check by endpoint
		if !s.scopeAllowed(key.Scopes, r) {
			writeError(w, http.StatusForbidden, "forbidden", "key lacks required scope for this endpoint")
			return
		}
		next.ServeHTTP(w, r)
	})
}

// scopeAllowed maps each route to its required scope.
func (s *Server) scopeAllowed(scopes []string, r *http.Request) bool {
	set := map[string]bool{}
	for _, sc := range scopes {
		set[sc] = true
	}
	has := func(sc string) bool { return set[sc] }
	path := r.URL.Path
	method := r.Method

	switch {
	case method == http.MethodGet && path == "/jobs":
		return has("jobs:read")
	case method == http.MethodPost && path == "/jobs":
		return has("jobs:write")
	case strings.HasPrefix(path, "/jobs/") && method == http.MethodGet:
		return has("jobs:read")
	case strings.HasPrefix(path, "/jobs/") && method == http.MethodPost:
		return has("jobs:write")
	case strings.HasPrefix(path, "/jobs/") && method == http.MethodDelete:
		return has("jobs:write")
	case strings.HasPrefix(path, "/jobs/") && method == http.MethodPatch:
		return has("jobs:write")
	case path == "/queue":
		return has("queue:read")
	case path == "/workers":
		return has("workers:read")
	case path == "/workers/scale":
		return has("workers:write")
	case strings.HasPrefix(path, "/storage/"):
		return has("storage:manage")
	case strings.HasPrefix(path, "/webhooks"):
		return has("webhooks:manage")
	case strings.HasPrefix(path, "/keys"):
		return has("keys:manage")
	case path == "/profiles":
		return has("profiles:read")
	case path == "/plugins":
		return has("plugins:read")
	case strings.HasPrefix(path, "/benchmark") || path == "/metrics" || path == "/system":
		return has("metrics:read")
	case strings.HasPrefix(path, "/config/"):
		return has("config:manage")
	case strings.HasPrefix(path, "/cron"):
		return has("cron:manage")
	}
	return false
}

// --- jobs ---

type createJobRequest struct {
	InputRef      string        `json:"input_ref"`
	Profile       string        `json:"profile"`
	DestinationID int           `json:"destination_id"`
	Priority      core.Priority `json:"priority"`
	// Lang is the subtitle language (e.g. "fa", "en"); applied to subtitle tasks.
	Lang string `json:"lang"`
	// SrcLang is the language the audio is actually spoken in (whisper's -l),
	// e.g. "en", "tr". When it differs from Lang, hybrid mode translates.
	SrcLang string `json:"src_lang"`
	// Name overrides the output base name of subtitle tasks (e.g. "movie").
	Name string `json:"name"`
	// Path is an optional subdirectory under the destination storage root
	// (e.g. "movie" or "series" for one server with many folders).
	Path string `json:"path"`
	// DeleteSource removes the source input file after the job completes.
	DeleteSource bool `json:"delete_source"`
	// Provider selects the ai-subtitle provider for this job: whisper | gemini |
	// hybrid (empty = server default).
	Provider string `json:"provider"`
	// TrimStart skips this many seconds from the beginning of the clip before
	// HLS packaging (0 = from the very start).
	TrimStart float64 `json:"trim_start"`
	// TrimEnd cuts this many seconds off the end of the clip (0 = keep to the
	// very end). Applied with TrimStart; either or both may be set.
	TrimEnd float64 `json:"trim_end"`
	// ThumbCount, when > 0, replaces the default poster/sprite/stills with
	// exactly this many evenly-spaced thumbnails.
	ThumbCount float64 `json:"thumb_count"`
	// ThumbSize selects the custom thumbnail dimensions: "1080x1080" or
	// "original" (keep source resolution). Ignored unless thumb_count or
	// thumb_at is set.
	ThumbSize string `json:"thumb_size"`
	// ThumbAt, when > 0, captures a single frame at this offset (seconds)
	// instead of the default poster/sprite/stills or the thumb_count set.
	ThumbAt float64 `json:"thumb_at"`
	// SourceServerID, when set, means InputRef is a relative path resolved
	// against this REGISTERED storage server's root (the same servers used
	// for job output, weft storage add) instead of a local filesystem path —
	// this is what makes trim/thumbnail/subtitle work against a source that
	// lives on ssh/s3, not just local disk.
	SourceServerID int `json:"source_server_id"`
	// Forced/Default set the subtitle #EXT-X-MEDIA flags for --profile
	// subtitle-add (ignored otherwise).
	Forced  bool `json:"forced"`
	Default bool `json:"default"`
}

func (s *Server) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req createJobRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.InputRef == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "input_ref is required")
		return
	}
	if req.Profile == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "profile is required")
		return
	}
	prof, err := profiles.Get(req.Profile)
	if err != nil {
		writeError(w, http.StatusBadRequest, "unknown_profile", err.Error())
		return
	}
	if req.Priority == "" {
		req.Priority = core.PriorityNormal
	}
	if !req.Priority.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_priority", fmt.Sprintf("priority %q unknown", req.Priority))
		return
	}
	// destination validation
	if req.DestinationID != 0 {
		servers, _ := s.store.ListStorageServers(r.Context())
		found := false
		for _, sv := range servers {
			if sv.ID == req.DestinationID {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusBadRequest, "unknown_destination", fmt.Sprintf("destination_id %d not registered", req.DestinationID))
			return
		}
	}
	// source server validation (same registry as destinations)
	if req.SourceServerID != 0 {
		servers, _ := s.store.ListStorageServers(r.Context())
		found := false
		for _, sv := range servers {
			if sv.ID == req.SourceServerID {
				found = true
				break
			}
		}
		if !found {
			writeError(w, http.StatusBadRequest, "unknown_source_server", fmt.Sprintf("source_server_id %d not registered", req.SourceServerID))
			return
		}
	}
	// provider validation (only meaningful for ai-subtitle jobs, but reject
	// typos early regardless of profile so callers notice the mistake).
	switch req.Provider {
	case "", "whisper", "gemini", "hybrid":
	default:
		writeError(w, http.StatusBadRequest, "invalid_provider", fmt.Sprintf("provider %q unknown (want whisper|gemini|hybrid)", req.Provider))
		return
	}

	jobID := core.JobID(core.NewID("job"))
	job := core.Job{
		ID:             jobID,
		Priority:       req.Priority,
		Profile:        prof.Name,
		DestinationID:  req.DestinationID,
		DestPath:       strings.Trim(req.Path, "/"),
		InputRef:       req.InputRef,
		DeleteSource:   req.DeleteSource,
		SourceServerID: req.SourceServerID,
	}

	tasks, err := s.buildTasks(r.Context(), prof, jobID, buildTaskParams{
		Lang: req.Lang, Name: req.Name, Provider: req.Provider, SrcLang: req.SrcLang,
		TrimStart: req.TrimStart, TrimEnd: req.TrimEnd,
		ThumbCount: req.ThumbCount, ThumbAt: req.ThumbAt, ThumbSize: req.ThumbSize,
		Forced: req.Forced, Default: req.Default,
	})
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}

	if err := s.sched.Submit(r.Context(), job, tasks); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	ids := make([]string, len(tasks))
	for i, t := range tasks {
		ids[i] = string(t.ID)
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"id":     jobID,
		"status": core.JobQueued,
		"tasks":  ids,
	})
}

// buildTasks constructs the DAG, inserting an ai_subtitle task when the input
// lacks a subtitle track and auto_generate is enabled. Lang (from the request)
// is attached to every subtitle task so the plugin can name its output dir.
// Provider (whisper|gemini|hybrid) overrides the ai-subtitle provider for jobs
// that need a different engine than the server default. srcLang is the spoken
// audio language; it drives whisper's -l and hybrid translation. trimStart /
// trimEnd reach the hls and thumbnail tasks so the renditions and the poster
// thumbnails come from the same window; thumbCount/thumbSize switch the
// thumbnail task into the custom N-thumbnails mode.
// buildTaskParams is buildTasks' input, broken out of createJobRequest so the
// two can evolve independently (buildTasks is also usable outside the HTTP
// layer if ever needed).
type buildTaskParams struct {
	Lang, Name, Provider, SrcLang    string
	TrimStart, TrimEnd               float64
	ThumbCount, ThumbAt              float64
	ThumbSize                        string
	Forced, Default                  bool
}

func (s *Server) buildTasks(ctx context.Context, p profiles.Profile, jobID core.JobID, bp buildTaskParams) ([]core.Task, error) {
	tasks, err := p.BuildTaskGraph(jobID)
	if err != nil {
		return nil, err
	}
	// apply the requested subtitle language/name to subtitle-ish tasks
	for i := range tasks {
		switch tasks[i].Kind {
		case "subtitle", "ai_subtitle", "update_master", "audio_encode", "hls", "poster_upload":
		default:
			continue
		}
		if tasks[i].Params == nil {
			tasks[i].Params = map[string]any{}
		}
		if bp.Lang != "" {
			tasks[i].Params["lang"] = bp.Lang
		}
		if bp.Name != "" {
			tasks[i].Params["name"] = bp.Name
		}
	}
	// trim window: applies to hls (the packager) and thumbnail (so the poster
	// stills match the trimmed renditions).
	if bp.TrimStart > 0 || bp.TrimEnd > 0 {
		for i := range tasks {
			if tasks[i].Kind != "hls" && tasks[i].Kind != "thumbnail" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			if bp.TrimStart > 0 {
				tasks[i].Params["trim_start"] = bp.TrimStart
			}
			if bp.TrimEnd > 0 {
				tasks[i].Params["trim_end"] = bp.TrimEnd
			}
		}
	}
	// custom thumbnail modes: only meaningful for the thumbnail task.
	// thumb_count (N evenly-spaced) and thumb_at (one frame at an offset)
	// are mutually exclusive; the plugin checks thumb_count first.
	if bp.ThumbCount > 0 || bp.ThumbAt > 0 {
		for i := range tasks {
			if tasks[i].Kind != "thumbnail" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			if bp.ThumbCount > 0 {
				tasks[i].Params["thumb_count"] = bp.ThumbCount
			}
			if bp.ThumbAt > 0 {
				tasks[i].Params["thumb_at"] = bp.ThumbAt
			}
			if bp.ThumbSize != "" {
				tasks[i].Params["thumb_size"] = bp.ThumbSize
			}
		}
	}
	if bp.Provider != "" {
		// the requested provider only matters for ai_subtitle tasks
		for i := range tasks {
			if tasks[i].Kind != "ai_subtitle" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			tasks[i].Params["provider"] = bp.Provider
		}
	}
	if bp.SrcLang != "" {
		// source language only matters for ai_subtitle tasks
		for i := range tasks {
			if tasks[i].Kind != "ai_subtitle" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			tasks[i].Params["src_lang"] = bp.SrcLang
		}
	}
	// forced/default subtitle track flags: only meaningful for update_master
	// (subtitle-add's master-playlist rewrite step).
	if bp.Forced || bp.Default {
		for i := range tasks {
			if tasks[i].Kind != "update_master" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			if bp.Forced {
				tasks[i].Params["forced"] = true
			}
			if bp.Default {
				tasks[i].Params["default"] = true
			}
		}
	}
	// auto AI subtitle insertion: only if the profile has a subtitle node,
	// auto_generate is on, and the input has no subtitle track.
	if hasKind(tasks, "subtitle") && s.cfg.AI.AutoGenerate.Enabled {
		if !s.inputHasSubtitles(ctx, p) {
			ai := core.Task{
				ID:        core.TaskID(core.NewID("task_ai_subtitle")),
				JobID:     jobID,
				Kind:      "ai_subtitle",
				Status:    core.TaskPending,
				DependsOn: []core.TaskID{findKindTask(tasks, "video_encode")},
				Params:    map[string]any{},
			}
			if bp.Lang != "" {
				ai.Params["lang"] = bp.Lang
			}
			if bp.Provider != "" {
				ai.Params["provider"] = bp.Provider
			}
			if bp.SrcLang != "" {
				ai.Params["src_lang"] = bp.SrcLang
			}
			// subtitle depends on ai_subtitle now; ai_subtitle depends on video
			for i := range tasks {
				if tasks[i].Kind == "subtitle" {
					tasks[i].DependsOn = append(tasks[i].DependsOn, ai.ID)
				}
			}
			tasks = append(tasks, ai)
		}
	}
	return tasks, nil
}

func (s *Server) inputHasSubtitles(ctx context.Context, p profiles.Profile) bool {
	// Without ffprobe access here we conservatively assume the input lacks
	// subtitles unless the profile params say otherwise — the ai-subtitle
	// contract is "fill a gap, don't duplicate". A real probe is done by the
	// executor at task time; this gate keeps auto-generation opt-in.
	return false
}

func hasKind(tasks []core.Task, kind string) bool {
	for _, t := range tasks {
		if t.Kind == kind {
			return true
		}
	}
	return false
}

func findKindTask(tasks []core.Task, kind string) core.TaskID {
	for _, t := range tasks {
		if t.Kind == kind {
			return t.ID
		}
	}
	return ""
}

type taskDTO struct {
	ID        string     `json:"id"`
	Kind      string     `json:"kind"`
	Status    string     `json:"status"`
	Progress  float64    `json:"progress_percent"`
	DependsOn []string   `json:"depends_on,omitempty"`
	Error     string     `json:"error,omitempty"`
	// StartedAt lets a client compute a naive linear ETA
	// (elapsed/(progress/100) - elapsed) without a dedicated endpoint — see
	// the CLI dashboard. Omitted for tasks that haven't started yet.
	StartedAt *time.Time `json:"started_at,omitempty"`
}

func (s *Server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	job, err := s.store.LoadJob(r.Context(), id)
	if errors.Is(err, core.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	tasks, err := s.store.ListTasks(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	dtos := make([]taskDTO, 0, len(tasks))
	var overall float64
	var totalWeight float64
	for _, t := range tasks {
		dtos = append(dtos, taskDTO{
			ID:        string(t.ID),
			Kind:      t.Kind,
			Status:    string(t.Status),
			Progress:  t.Progress,
			DependsOn: toStrings(t.DependsOn),
			Error:     t.Error,
			StartedAt: t.StartedAt,
		})
		totalWeight++
		if t.Status == core.TaskDone {
			overall += 100
		} else {
			overall += t.Progress
		}
	}
	var op float64
	if totalWeight > 0 {
		op = overall / totalWeight
	}
	outputs, err := s.store.ListJobOutputs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	assetDTOs := make([]map[string]any, 0, len(outputs))
	for _, a := range outputs {
		assetDTOs = append(assetDTOs, map[string]any{
			"kind": a.Kind,
			"name": a.Name,
			"uri":  a.URI,
			"dir":  a.Dir,
			"bytes": a.Bytes,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               job.ID,
		"status":           job.Status,
		"priority":         job.Priority,
		"profile":          job.Profile,
		"input_ref":        job.InputRef,
		"destination_id":   job.DestinationID,
		"source_server_id": job.SourceServerID,
		"verified":         job.Verified,
		"overall_progress": op,
		"error":            job.Error,
		"tasks":            dtos,
		"assets":           assetDTOs,
	})
}

func toStrings(ids []core.TaskID) []string {
	out := make([]string, len(ids))
	for i, id := range ids {
		out[i] = string(id)
	}
	return out
}

func (s *Server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	filter := core.JobFilter{
		Status:   core.JobStatus(q.Get("status")),
		Priority: core.Priority(q.Get("priority")),
		Cursor:   q.Get("cursor"),
	}
	if l := q.Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			filter.Limit = n
		}
	}
	jobs, err := s.store.ListJobs(r.Context(), filter)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// include_progress is opt-in: computing it needs one ListTasks call per
	// returned job, which is fine for a filtered handful (e.g. the CLI
	// dashboard's ?status=running poll) but would reintroduce an N+1 if it
	// ran unconditionally on every unfiltered `weft jobs list`.
	includeProgress := q.Get("include_progress") == "true"
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		row := map[string]any{
			"id":         j.ID,
			"status":     j.Status,
			"priority":   j.Priority,
			"profile":    j.Profile,
			"verified":   j.Verified,
			"created_at": j.CreatedAt,
		}
		if includeProgress {
			row["overall_progress"] = s.jobOverallProgress(r.Context(), j.ID)
		}
		out = append(out, row)
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
}

// jobOverallProgress averages per-task progress (done tasks count as 100),
// matching handleGetJob's calculation. Errors collapse to 0 rather than
// failing the whole list response for one bad job.
func (s *Server) jobOverallProgress(ctx context.Context, id core.JobID) float64 {
	tasks, err := s.store.ListTasks(ctx, id)
	if err != nil || len(tasks) == 0 {
		return 0
	}
	var sum float64
	for _, t := range tasks {
		if t.Status == core.TaskDone {
			sum += 100
		} else {
			sum += t.Progress
		}
	}
	return sum / float64(len(tasks))
}

func (s *Server) handleJobEvents(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	evs, err := s.store.ListEvents(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(evs))
	for _, e := range evs {
		out = append(out, map[string]any{
			"id":         e.ID,
			"kind":       e.Kind,
			"payload":    json.RawMessage(e.Payload),
			"created_at": e.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": out})
}

// handleUpdateJobPriority changes a queued/reserved job's priority so it can
// move up (or down) the priority bands NextReady schedules by, without
// cancelling and resubmitting the job. Once a job has started running,
// reordering the queue can no longer affect it, so this is rejected past that
// point (409) rather than silently accepted and ignored.
func (s *Server) handleUpdateJobPriority(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	var req struct {
		Priority core.Priority `json:"priority"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if !req.Priority.Valid() {
		writeError(w, http.StatusBadRequest, "invalid_priority", fmt.Sprintf("unknown priority %q", req.Priority))
		return
	}
	job, err := s.store.LoadJob(r.Context(), id)
	if errors.Is(err, core.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	switch job.Status {
	case core.JobQueued, core.JobReserved:
	default:
		writeError(w, http.StatusConflict, "job_not_queued",
			fmt.Sprintf("job %s is %s; priority can only change while queued or reserved", job.ID, job.Status))
		return
	}
	ok, err := s.store.UpdateJobPriority(r.Context(), id, req.Priority)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if !ok {
		writeError(w, http.StatusConflict, "job_not_queued",
			fmt.Sprintf("job %s left the queued/reserved state before the priority change applied", job.ID))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(id), "priority": string(req.Priority)})
}

func (s *Server) handleJobAction(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	action := chi.URLParam(r, "action")

	job, err := s.store.LoadJob(r.Context(), id)
	if errors.Is(err, core.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}

	var to core.JobStatus
	switch action {
	case "cancel":
		to = core.JobCancelled
	case "retry":
		to = core.JobRetry
	case "pause":
		to = core.JobPaused
	case "resume":
		to = core.JobResumed
	default:
		writeError(w, http.StatusBadRequest, "invalid_action", "action must be cancel|retry|pause|resume")
		return
	}
	if err := s.sm.Transition(r.Context(), job.ID, to, "user action"); err != nil {
		writeError(w, http.StatusConflict, "invalid_transition", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(id), "status": string(to)})
}

// handleDeleteJob removes a job and all of its data (tasks, events, outputs,
// logs) from the store. Deleting is an explicit operator action for finished
// jobs so history doesn't accumulate forever; it is refused for jobs that are
// still actively processing (cancel those first).
func (s *Server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	job, err := s.store.LoadJob(r.Context(), id)
	if errors.Is(err, core.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	switch job.Status {
	case core.JobQueued, core.JobReserved, core.JobRunning, core.JobUploading, core.JobPaused, core.JobResumed:
		writeError(w, http.StatusConflict, "job_active",
			fmt.Sprintf("job %s is %s; cancel it before deleting", job.ID, job.Status))
		return
	}
	// By default also remove the job's published files from destination
	// storage — otherwise DeleteJob only clears DB rows and every asset it
	// ever produced is orphaned on local/ssh/s3 forever. ?purge_files=false
	// opts out (e.g. when the destination path is shared/symlinked with
	// something else that still needs the files).
	purgeFiles := r.URL.Query().Get("purge_files") != "false"
	if purgeFiles && s.stbuild != nil {
		if outputs, oerr := s.store.ListJobOutputs(r.Context(), id); oerr == nil {
			if st, berr := s.stbuild(r.Context(), job.DestinationID, strings.Trim(job.DestPath, "/")); berr == nil {
				for _, a := range outputs {
					if derr := st.Delete(r.Context(), a); derr != nil {
						// best-effort: a flaky/unreachable remote must not block
						// clearing the job's DB record, so this is logged, not fatal.
						log.Printf("delete job %s: purge asset %s: %v", id, a.Name, derr)
					}
				}
			} else {
				log.Printf("delete job %s: build storage for purge: %v", id, berr)
			}
		} else {
			log.Printf("delete job %s: list outputs for purge: %v", id, oerr)
		}
	}
	if err := s.store.DeleteJob(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": string(id), "status": "deleted"})
}

// handleTaskLog returns the saved execution log for a single task.
func (s *Server) handleTaskLog(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	taskID := core.TaskID(chi.URLParam(r, "taskID"))
	// verify the task belongs to the job so callers can't read across jobs
	task, err := s.store.LoadTask(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusNotFound, "task_not_found", "no such task")
		return
	}
	if task.JobID != id {
		writeError(w, http.StatusNotFound, "task_not_found", "task does not belong to this job")
		return
	}
	log, err := s.store.LoadTaskLog(r.Context(), taskID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"task_id": string(taskID),
		"log":     log,
	})
}

// maxAssetBytes bounds handleGetAsset's base64 response so a caller can't
// pull an arbitrarily large produced file (e.g. a full .ts/.mp4) into one
// in-memory JSON payload.
const maxAssetBytes = 10 * 1024 * 1024

// handleGetAsset returns one produced asset of a job as a base64-encoded data
// URI, so a caller can grab a thumbnail (or any small output) directly from
// the API without touching storage. The asset must already be uploaded; the
// file is read back through the same destination storage the job published to.
func (s *Server) handleGetAsset(w http.ResponseWriter, r *http.Request) {
	id := core.JobID(chi.URLParam(r, "id"))
	name := chi.URLParam(r, "name")
	job, err := s.store.LoadJob(r.Context(), id)
	if errors.Is(err, core.ErrJobNotFound) {
		writeError(w, http.StatusNotFound, "job_not_found", "no such job")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	outputs, err := s.store.ListJobOutputs(r.Context(), id)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	var match *core.AssetRef
	for i := range outputs {
		if outputs[i].Name == name || outputs[i].Dir+"/"+outputs[i].Name == name {
			match = &outputs[i]
			break
		}
	}
	if match == nil {
		writeError(w, http.StatusNotFound, "asset_not_found", "no such asset for this job")
		return
	}
	// A caller could otherwise request an arbitrarily large produced asset
	// (a full .ts/.mp4 segment) and get it base64-encoded into one JSON
	// response with no bound. This endpoint is for small assets (thumbnails,
	// subtitles, playlists) — anything bigger should be fetched from storage
	// directly.
	if match.Bytes > maxAssetBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "asset_too_large",
			fmt.Sprintf("asset %s is %d bytes, exceeds the %d byte limit for base64 delivery", match.Name, match.Bytes, maxAssetBytes))
		return
	}
	if s.stbuild == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "storage builder not configured")
		return
	}
	st, err := s.stbuild(r.Context(), job.DestinationID, strings.Trim(job.DestPath, "/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	rc, err := st.Open(r.Context(), *match)
	if err != nil {
		writeError(w, http.StatusNotFound, "asset_unavailable", err.Error())
		return
	}
	defer rc.Close()
	// Defense in depth in case match.Bytes was unset/stale for some storage
	// backend: never read more than the cap regardless of the recorded size.
	data, err := io.ReadAll(io.LimitReader(rc, maxAssetBytes+1))
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	if int64(len(data)) > maxAssetBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "asset_too_large",
			fmt.Sprintf("asset %s exceeds the %d byte limit for base64 delivery", match.Name, maxAssetBytes))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"name": match.Name,
		"uri":  match.URI,
		"mime": mimeType(match.Name),
		"data": base64.StdEncoding.EncodeToString(data),
	})
}

// mimeType guesses a media type from a file name for the asset endpoint.
func mimeType(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".webp":
		return "image/webp"
	case ".gif":
		return "image/gif"
	case ".vtt":
		return "text/vtt"
	case ".m3u8":
		return "application/vnd.apple.mpegurl"
	case ".ts":
		return "video/mp2t"
	case ".mp4":
		return "video/mp4"
	case ".mp3":
		return "audio/mpeg"
	case ".srt":
		return "text/plain"
	}
	return "application/octet-stream"
}

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	jobs, err := s.store.ListActiveJobs(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	byPriority := map[string]int{}
	for _, j := range jobs {
		if j.Status == core.JobQueued || j.Status == core.JobReserved {
			byPriority[string(j.Priority)]++
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"queue": byPriority})
}

func (s *Server) handleWorkers(w http.ResponseWriter, r *http.Request) {
	if s.worker == nil {
		writeJSON(w, http.StatusOK, map[string]any{"workers": []WorkerState{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": s.worker.Snapshot()})
}

// handleScaleWorkers resizes the worker pool at runtime (no daemon restart).
// count is bounded below by 1 and above by the config workers.max (when set).
func (s *Server) handleScaleWorkers(w http.ResponseWriter, r *http.Request) {
	if s.scaler == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "worker scaling is not enabled")
		return
	}
	var req struct {
		Count int `json:"count"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Count < 1 {
		writeError(w, http.StatusBadRequest, "invalid_request", "count must be >= 1")
		return
	}
	if s.cfg.Workers.Max > 0 && req.Count > s.cfg.Workers.Max {
		writeError(w, http.StatusBadRequest, "invalid_request",
			fmt.Sprintf("count %d exceeds workers.max %d", req.Count, s.cfg.Workers.Max))
		return
	}
	if err := s.scaler.Scale(r.Context(), req.Count); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"workers": req.Count})
}

// --- storage servers ---

func (s *Server) handleAddServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     int            `json:"id"`
		Type   string         `json:"type"`
		Host   string         `json:"host"`
		User   string         `json:"user"`
		Config map[string]any `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.ID == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "id is required")
		return
	}
	if req.Type == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "type is required (ssh|local|s3|minio|r2)")
		return
	}
	if err := s.store.SaveStorageServer(r.Context(), sqlite.StorageServer{
		ID:        req.ID,
		Type:      req.Type,
		Host:      req.Host,
		User:      req.User,
		Config:    req.Config,
		CreatedAt: time.Now().UTC(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": req.ID, "status": "registered"})
}

func (s *Server) handleListServers(w http.ResponseWriter, r *http.Request) {
	servers, err := s.store.ListStorageServers(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(servers))
	for _, sv := range servers {
		// never return key material
		out = append(out, map[string]any{
			"id":   sv.ID,
			"type": sv.Type,
			"host": sv.Host,
			"user": sv.User,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"servers": out})
}

// handleRebuildMaster scans a destination storage directory and regenerates
// its master playlist (playlist.m3u8) from the files actually present.
func (s *Server) handleRebuildMaster(w http.ResponseWriter, r *http.Request) {
	if s.stbuild == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "storage builder not configured")
		return
	}
	var req struct {
		DestinationID int    `json:"destination_id"`
		Path          string `json:"path"`
		Codec         string `json:"codec"` // "h264" (default) or "hevc"
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	st, err := s.stbuild(r.Context(), req.DestinationID, strings.Trim(req.Path, "/"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	p, err := rebuild.Rebuild(r.Context(), st, req.Codec)
	if err != nil {
		writeError(w, http.StatusUnprocessableEntity, "rebuild_failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":     "ok",
		"renditions": p.Renditions,
		"subtitles":  p.Subtitles,
		"audios":     p.Audios,
		"master":     p.Master,
	})
}

// --- webhooks ---

func (s *Server) handleCreateWebhook(w http.ResponseWriter, r *http.Request) {
	var req struct {
		URL    string   `json:"url"`
		Secret string   `json:"secret"`
		Events []string `json:"events"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.URL == "" || len(req.Events) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "url and events are required")
		return
	}
	id := core.NewID("wh")
	if err := s.store.SaveWebhook(r.Context(), sqlite.Webhook{
		ID:             id,
		URL:            req.URL,
		Secret:         req.Secret,
		Events:         req.Events,
		MaxRetries:     8,
		TimeoutSeconds: 10,
		CreatedAt:      time.Now().UTC(),
	}); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"id": id})
}

func (s *Server) handleListWebhooks(w http.ResponseWriter, r *http.Request) {
	whs, err := s.store.ListWebhooks(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(whs))
	for _, w := range whs {
		out = append(out, map[string]any{
			"id":     w.ID,
			"url":    w.URL,
			"events": w.Events,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

func (s *Server) handleDeleteWebhook(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteWebhook(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

func (s *Server) handleReplayWebhook(w http.ResponseWriter, r *http.Request) {
	eventID := chi.URLParam(r, "event_id")
	// find outbox rows referencing this event and reset them
	rows, _ := s.store.Outbox().ListPending(r.Context(), 1000, time.Now().UTC().Add(time.Hour))
	ob := s.store.Outbox().(interface {
		ResetToPending(context.Context, string) error
	})
	reset := 0
	for _, row := range rows {
		if row.EventID == eventID && row.Status == "dead_letter" {
			if err := ob.ResetToPending(r.Context(), row.ID); err == nil {
				reset++
			}
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"event_id": eventID, "replayed": reset})
}

// --- API keys ---

func (s *Server) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", "malformed JSON body")
		return
	}
	if req.Name == "" || len(req.Scopes) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "name and scopes are required")
		return
	}
	raw, id, err := s.keys.Create(req.Name, req.Scopes)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	// raw value returned exactly once
	writeJSON(w, http.StatusCreated, map[string]string{"id": id, "key": raw})
}

func (s *Server) handleListKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := s.store.ListAPIKeys(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	out := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		out = append(out, map[string]any{"id": k.ID, "name": k.Name, "scopes": k.Scopes})
	}
	writeJSON(w, http.StatusOK, map[string]any{"keys": out})
}

func (s *Server) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := s.store.DeleteAPIKey(r.Context(), id); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"id": id, "status": "deleted"})
}

// --- profiles / plugins ---

func (s *Server) handleListProfiles(w http.ResponseWriter, r *http.Request) {
	ps := profiles.List()
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		out = append(out, map[string]any{"name": p.Name, "description": p.Description})
	}
	writeJSON(w, http.StatusOK, map[string]any{"profiles": out})
}

func (s *Server) handleListPlugins(w http.ResponseWriter, r *http.Request) {
	ps := s.registry.List()
	out := make([]map[string]any, 0, len(ps))
	for _, p := range ps {
		c := p.Capabilities()
		out = append(out, map[string]any{"name": c.Name, "kinds": c.SupportedKinds})
	}
	writeJSON(w, http.StatusOK, map[string]any{"plugins": out})
}

// --- config export/import ---

// handleConfigExport returns the running config as YAML — the same shape
// weft.yaml is authored in, so it round-trips straight into `config import`
// or `--config`. Secrets (admin API key, webhook secrets, the Gemini API key)
// are redacted by default; ?include_secrets=true includes them in plaintext,
// same authenticated+scoped request either way.
func (s *Server) handleConfigExport(w http.ResponseWriter, r *http.Request) {
	if s.cfg == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "no config loaded")
		return
	}
	export := *s.cfg
	if r.URL.Query().Get("include_secrets") != "true" {
		export.Security.AdminAPIKey = redactedIfSet(export.Security.AdminAPIKey)
		export.AI.Gemini.APIKey = redactedIfSet(export.AI.Gemini.APIKey)
		for i := range export.Webhooks {
			export.Webhooks[i].Secret = redactedIfSet(export.Webhooks[i].Secret)
		}
	}
	b, err := yaml.Marshal(export)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/yaml")
	w.WriteHeader(http.StatusOK)
	w.Write(b)
}

func redactedIfSet(s string) string {
	if s == "" {
		return ""
	}
	return "<redacted>"
}

// handleConfigImport validates a full weft.yaml body and writes it to the
// path this server was started with. It does NOT hot-reload — most config
// (listen address, worker pool, plugins, storage) is only read at startup, so
// attempting partial live reconfiguration would silently diverge from what's
// on disk. The response says as much; the operator restarts to apply it.
func (s *Server) handleConfigImport(w http.ResponseWriter, r *http.Request) {
	if s.cfgPath == "" {
		writeError(w, http.StatusNotImplemented, "not_configured", "server was not started with a config file path; config import is unavailable")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	var next cfg.Config
	if err := yaml.Unmarshal(body, &next); err != nil {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("invalid config schema: %v", err))
		return
	}
	// a redacted secret imported verbatim would otherwise silently disable
	// auth/signing/AI access on the next restart — refuse rather than guess.
	if next.Security.AdminAPIKey == "<redacted>" || next.AI.Gemini.APIKey == "<redacted>" {
		writeError(w, http.StatusBadRequest, "invalid_request", "config contains a redacted secret placeholder (<redacted>) — export with ?include_secrets=true or fill in the real value before importing")
		return
	}
	for _, wh := range next.Webhooks {
		if wh.Secret == "<redacted>" {
			writeError(w, http.StatusBadRequest, "invalid_request", "config contains a redacted webhook secret placeholder (<redacted>) — export with ?include_secrets=true or fill in the real value before importing")
			return
		}
	}
	if probs := next.Validate(); len(probs) > 0 {
		writeError(w, http.StatusBadRequest, "invalid_config", strings.Join(probs, "; "))
		return
	}
	if err := os.WriteFile(s.cfgPath, body, 0o644); err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"status":  "written",
		"path":    s.cfgPath,
		"applied": false,
		"note":    "config written to disk; restart the agent (weft serve) to apply it",
	})
}

// --- cron ---

// handleListCron reports each scheduled job's schedule, last run, next run,
// and last error (if any). Mirrors handleWorkers' nil-handle pattern: no
// scheduler configured just means an empty list, not an error.
func (s *Server) handleListCron(w http.ResponseWriter, r *http.Request) {
	if s.cronSched == nil {
		writeJSON(w, http.StatusOK, map[string]any{"jobs": []cron.Status{}})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": s.cronSched.List()})
}

// handleRunCron triggers a named cron job immediately, sharing the exact same
// run + bookkeeping path the scheduler's own ticker uses (Scheduler.RunNow).
func (s *Server) handleRunCron(w http.ResponseWriter, r *http.Request) {
	if s.cronSched == nil {
		writeError(w, http.StatusNotImplemented, "not_configured", "no cron scheduler configured")
		return
	}
	name := chi.URLParam(r, "job")
	runErr := s.cronSched.RunNow(r.Context(), name)
	if errors.Is(runErr, cron.ErrUnknownJob) {
		writeError(w, http.StatusNotFound, "cron_job_not_found", runErr.Error())
		return
	}
	out := map[string]string{"job": name, "status": "ran"}
	if runErr != nil {
		// the job WAS found and DID run — this is its own failure, not a
		// routing error, so 200 with the error surfaced beats a 5xx here.
		out["status"] = "failed"
		out["error"] = runErr.Error()
	}
	writeJSON(w, http.StatusOK, out)
}

// --- metrics / benchmark ---

func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write([]byte(s.metrics.Render()))
}

// handleSystem reports host CPU/memory/disk usage. The disk figures refer to
// the storage base path (or CWD when the config leaves it empty).
func (s *Server) handleSystem(w http.ResponseWriter, r *http.Request) {
	path := s.cfg.Storage.Local.BasePath
	if path == "" {
		path = "."
	}
	snap, err := sysinfo.Collect(path)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "system_error", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, snap)
}

// RunBenchmark runs a fresh node benchmark and persists it. Exported so the
// cron scheduler's "benchmark" job (daemon package) shares this exact logic
// with POST /benchmark instead of duplicating it.
func (s *Server) RunBenchmark(ctx context.Context) (sqlite.Benchmark, error) {
	b := runBenchmark()
	if err := s.store.SaveBenchmark(ctx, b); err != nil {
		return sqlite.Benchmark{}, err
	}
	return b, nil
}

func (s *Server) handleRunBenchmark(w http.ResponseWriter, r *http.Request) {
	b, err := s.RunBenchmark(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "internal_error", err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"cpu_score": b.CPUScore, "ffmpeg_score": b.FFmpegScore})
}

func (s *Server) handleGetBenchmark(w http.ResponseWriter, r *http.Request) {
	b, err := s.store.LastBenchmark(r.Context())
	if err != nil {
		writeError(w, http.StatusNotFound, "no_benchmark", "no benchmark has run yet")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"cpu_score":     b.CPUScore,
		"ffmpeg_score":  b.FFmpegScore,
		"disk_io_score": b.DiskIOScore,
		"memory_score":  b.MemoryScore,
		"created_at":    b.CreatedAt,
	})
}
