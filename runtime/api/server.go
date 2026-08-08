// Package api implements the REST API (chi) and API-key auth. The CLI is a
// thin client over exactly these handlers — nothing is CLI-only.
package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/rebuild"
	"github.com/mohammadjaf013/weft/profiles"
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
	keys     *KeyManager
	worker   WorkerHandle
	stbuild  StorageBuilder
}

// StorageBuilder constructs the destination storage for a registered server id
// (or destination 0 = default local). Injected by the daemon so the api
// package stays free of storage-plugin imports.
type StorageBuilder func(ctx context.Context, destinationID int, destPath string) (core.Storage, error)

// WorkerHandle lets the API report current worker state to /workers.
type WorkerHandle interface {
	Snapshot() []WorkerState
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
	Keys     *KeyManager
	Worker   WorkerHandle
	// Storage builds destination storage for a server id / subdir (nil ⇒ no
	// storage-admin endpoints such as rebuild-master).
	Storage StorageBuilder
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
		keys:     o.Keys,
		worker:   o.Worker,
		stbuild:  o.Storage,
	}
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
		pr.Post("/jobs/{id}/{action}", s.handleJobAction) // jobs:write

		pr.Get("/queue", s.handleQueue)     // queue:read
		pr.Get("/workers", s.handleWorkers) // workers:read

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
	case path == "/queue":
		return has("queue:read")
	case path == "/workers":
		return has("workers:read")
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
		ID:            jobID,
		Priority:      req.Priority,
		Profile:       prof.Name,
		DestinationID: req.DestinationID,
		DestPath:      strings.Trim(req.Path, "/"),
		InputRef:      req.InputRef,
		DeleteSource:  req.DeleteSource,
	}

	tasks, err := s.buildTasks(r.Context(), prof, jobID, req.Lang, req.Name, req.Provider, req.SrcLang)
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
// audio language; it drives whisper's -l and hybrid translation.
func (s *Server) buildTasks(ctx context.Context, p profiles.Profile, jobID core.JobID, lang, name, provider, srcLang string) ([]core.Task, error) {
	tasks, err := p.BuildTaskGraph(jobID)
	if err != nil {
		return nil, err
	}
	// apply the requested subtitle language/name to subtitle-ish tasks
	for i := range tasks {
		switch tasks[i].Kind {
		case "subtitle", "ai_subtitle", "update_master", "audio_encode", "hls":
		default:
			continue
		}
		if tasks[i].Params == nil {
			tasks[i].Params = map[string]any{}
		}
		if lang != "" {
			tasks[i].Params["lang"] = lang
		}
		if name != "" {
			tasks[i].Params["name"] = name
		}
	}
	if provider != "" {
		// the requested provider only matters for ai_subtitle tasks
		for i := range tasks {
			if tasks[i].Kind != "ai_subtitle" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			tasks[i].Params["provider"] = provider
		}
	}
	if srcLang != "" {
		// source language only matters for ai_subtitle tasks
		for i := range tasks {
			if tasks[i].Kind != "ai_subtitle" {
				continue
			}
			if tasks[i].Params == nil {
				tasks[i].Params = map[string]any{}
			}
			tasks[i].Params["src_lang"] = srcLang
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
			if lang != "" {
				ai.Params["lang"] = lang
			}
			if provider != "" {
				ai.Params["provider"] = provider
			}
			if srcLang != "" {
				ai.Params["src_lang"] = srcLang
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
	ID        string   `json:"id"`
	Kind      string   `json:"kind"`
	Status    string   `json:"status"`
	Progress  float64  `json:"progress_percent"`
	DependsOn []string `json:"depends_on,omitempty"`
	Error     string   `json:"error,omitempty"`
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
	tasks, _ := s.store.ListTasks(r.Context(), id)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"id":               job.ID,
		"status":           job.Status,
		"priority":         job.Priority,
		"profile":          job.Profile,
		"input_ref":        job.InputRef,
		"destination_id":   job.DestinationID,
		"verified":         job.Verified,
		"overall_progress": op,
		"error":            job.Error,
		"tasks":            dtos,
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
	out := make([]map[string]any, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, map[string]any{
			"id":         j.ID,
			"status":     j.Status,
			"priority":   j.Priority,
			"profile":    j.Profile,
			"verified":   j.Verified,
			"created_at": j.CreatedAt,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"jobs": out})
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

func (s *Server) handleQueue(w http.ResponseWriter, r *http.Request) {
	jobs, _ := s.store.ListJobs(r.Context(), core.JobFilter{})
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

func (s *Server) handleRunBenchmark(w http.ResponseWriter, r *http.Request) {
	b := runBenchmark()
	store := s.store
	if err := store.SaveBenchmark(r.Context(), b); err != nil {
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
