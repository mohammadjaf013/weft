package cli

import (
	"encoding/json"
	"fmt"
)

// cmdJobs handles `weft jobs ...`: list, get, create, events, log, asset, delete, action, priority.
func cmdJobs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("jobs: missing subcommand (list|get|create|events|log|asset|delete|action|priority)")
	}
	sub := args[0]
	rest := args[1:]
	switch sub {
	case "list":
		return jobsList(rest)
	case "get":
		return jobsGet(rest)
	case "create":
		return jobsCreate(rest)
	case "events":
		return jobsEvents(rest)
	case "log":
		return jobsLog(rest)
	case "asset":
		return jobsAsset(rest)
	case "delete":
		return jobsDelete(rest)
	case "action":
		return jobsAction(rest)
	case "priority":
		return jobsPriority(rest)
	default:
		return fmt.Errorf("jobs: unknown subcommand %q (list|get|create|events|log|asset|delete|action|priority)", sub)
	}
}

func jobsList(args []string) error {
	fs := remoteFlagSet("jobs list")
	status := fs.String("status", "", "filter by status")
	priority := fs.String("priority", "", "filter by priority")
	limit := fs.Int("limit", 0, "max results")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)

	path := "/jobs"
	sep := "?"
	if *status != "" || *priority != "" || *limit > 0 {
		first := true
		add := func(k, v string) {
			if !first {
				path += "&"
			}
			first = false
			path += k + "=" + v
		}
		if *status != "" {
			add("status", *status)
		}
		if *priority != "" {
			add("priority", *priority)
		}
		if *limit > 0 {
			add("limit", fmt.Sprintf("%d", *limit))
		}
		_ = sep
	}
	var out struct {
		Jobs []struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			Priority  string `json:"priority"`
			Profile   string `json:"profile"`
			Verified  bool   `json:"verified"`
			CreatedAt string `json:"created_at"`
		} `json:"jobs"`
	}
	if err := c.get(path, &out); err != nil {
		return err
	}
	if len(out.Jobs) == 0 {
		fmt.Println("no jobs")
		return nil
	}
	w := newTable("ID", "STATUS", "PRIORITY", "PROFILE", "CREATED")
	for _, j := range out.Jobs {
		w.row(j.ID, j.Status, j.Priority, j.Profile, j.CreatedAt)
	}
	w.print()
	return nil
}

func jobsGet(args []string) error {
	fs := remoteFlagSet("jobs get")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("jobs get: job id argument is required")
	}
	c := newClient(rf)
	var out struct {
		ID              string  `json:"id"`
		Status          string  `json:"status"`
		Priority        string  `json:"priority"`
		Profile         string  `json:"profile"`
		InputRef        string  `json:"input_ref"`
		DestinationID   int     `json:"destination_id"`
		Verified        bool    `json:"verified"`
		OverallProgress float64 `json:"overall_progress"`
		Error           string  `json:"error"`
		Tasks           []struct {
			ID       string  `json:"id"`
			Kind     string  `json:"kind"`
			Status   string  `json:"status"`
			Progress float64 `json:"progress_percent"`
			Error    string  `json:"error,omitempty"`
		} `json:"tasks"`
		Assets []struct {
			Kind  string `json:"kind"`
			Name  string `json:"name"`
			URI   string `json:"uri"`
			Bytes int64  `json:"bytes"`
		} `json:"assets"`
	}
	if err := c.get("/jobs/"+pos[0], &out); err != nil {
		return err
	}
	fmt.Printf("job %s  status=%s  priority=%s  profile=%s  verified=%v\n",
		out.ID, out.Status, out.Priority, out.Profile, out.Verified)
	fmt.Printf("  input=%s  destination=%d  progress=%.1f%%\n",
		out.InputRef, out.DestinationID, out.OverallProgress)
	if out.Error != "" {
		fmt.Printf("  error=%s\n", out.Error)
	}
	w := newTable("TASK", "KIND", "STATUS", "PROGRESS", "ERROR")
	for _, t := range out.Tasks {
		w.row(t.ID, t.Kind, t.Status, fmt.Sprintf("%.1f%%", t.Progress), t.Error)
	}
	w.print()
	if len(out.Assets) > 0 {
		aw := newTable("KIND", "NAME", "URI")
		for _, a := range out.Assets {
			aw.row(a.Kind, a.Name, a.URI)
		}
		aw.print()
	}
	return nil
}

func jobsCreate(args []string) error {
	fs := remoteFlagSet("jobs create")
	profile := fs.String("profile", "", "profile name (required)")
	priority := fs.String("priority", "normal", "priority (emergency|high|normal|low|background)")
	dest := fs.Int("destination", 0, "destination storage server id (0 = default local)")
	lang := fs.String("lang", "", "subtitle target language, e.g. fa, en (default auto)")
	srcLang := fs.String("src-lang", "", "language the audio is spoken in (whisper -l), e.g. en, tr; differs from --lang = translate")
	name := fs.String("name", "", "override the output base name so re-submitted files replace the same track (e.g. movie)")
	path := fs.String("path", "", "subdirectory under the destination storage root, e.g. movie or series")
	deleteSource := fs.Bool("delete-source", false, "delete the source input file after the job completes")
	provider := fs.String("provider", "", "ai-subtitle provider: whisper | gemini | hybrid (empty = server default)")
	trimStart := fs.Float64("trim-start", 0, "skip N seconds from the beginning of the clip before HLS packaging")
	trimEnd := fs.Float64("trim-end", 0, "cut N seconds off the end of the clip before HLS packaging")
	thumbCount := fs.Int("thumb-count", 0, "produce exactly N evenly-spaced thumbnails instead of poster/sprite/stills")
	thumbSize := fs.String("thumb-size", "", "custom thumbnail size: 1080x1080 or original (requires --thumb-count or --thumb-at)")
	thumbAt := fs.Float64("thumb-at", 0, "capture a single frame at this offset in seconds instead of poster/sprite/stills")
	sourceServer := fs.Int("source-server", 0, "fetch input_ref as a relative path from this registered storage server instead of local disk")
	forced := fs.Bool("forced", false, "mark this subtitle track FORCED=YES (--profile subtitle-add)")
	defaultTrack := fs.Bool("default", false, "mark this subtitle track DEFAULT=YES (--profile subtitle-add)")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	if *profile == "" {
		return fmt.Errorf("jobs create: --profile is required")
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("jobs create: input_ref argument is required")
	}
	c := newClient(rf)
	var out struct {
		ID     string   `json:"id"`
		Status string   `json:"status"`
		Tasks  []string `json:"tasks"`
	}
	body := jobsCreateBody(jobsCreateOpts{
		InputRef: pos[0], Profile: *profile, Priority: *priority, Dest: *dest,
		Lang: *lang, SrcLang: *srcLang, Name: *name, Path: *path,
		DeleteSource: *deleteSource, Provider: *provider,
		TrimStart: *trimStart, TrimEnd: *trimEnd,
		ThumbCount: *thumbCount, ThumbSize: *thumbSize, ThumbAt: *thumbAt,
		SourceServer: *sourceServer,
		Forced:       *forced, DefaultTrack: *defaultTrack,
	})
	err = c.post("/jobs", body, &out)
	if err != nil {
		return err
	}
	fmt.Printf("job %s (%s)\n", out.ID, out.Status)
	for _, t := range out.Tasks {
		fmt.Printf("  task %s\n", t)
	}
	return nil
}

// jobsCreateOpts holds every `weft jobs create` flag. A struct (not another
// positional parameter) because this list has grown past the point where
// positional args stay readable — see jobsCreateBody.
type jobsCreateOpts struct {
	InputRef, Profile, Priority string
	Dest                        int
	Lang, SrcLang, Name, Path   string
	DeleteSource                bool
	Provider                    string
	TrimStart, TrimEnd          float64
	ThumbCount                  int
	ThumbSize                   string
	// ThumbAt, when > 0, captures a single frame at this offset (seconds)
	// instead of the default poster/sprite/stills or the --thumb-count set.
	ThumbAt float64
	// SourceServer, when set, means InputRef is a relative path resolved
	// against this registered storage server instead of a local path.
	SourceServer int
	// Forced/DefaultTrack set the subtitle #EXT-X-MEDIA flags for
	// --profile subtitle-add (ignored otherwise).
	Forced       bool
	DefaultTrack bool
}

// jobsCreateBody builds the /jobs payload from CLI flags (separate so the flag
// plumbing is unit-testable without an HTTP round trip).
func jobsCreateBody(o jobsCreateOpts) map[string]any {
	body := map[string]any{
		"input_ref":      o.InputRef,
		"profile":        o.Profile,
		"priority":       o.Priority,
		"destination_id": o.Dest,
	}
	if o.Lang != "" {
		body["lang"] = o.Lang
	}
	if o.SrcLang != "" {
		body["src_lang"] = o.SrcLang
	}
	if o.Name != "" {
		body["name"] = o.Name
	}
	if o.Path != "" {
		body["path"] = o.Path
	}
	if o.DeleteSource {
		body["delete_source"] = true
	}
	if o.Provider != "" {
		body["provider"] = o.Provider
	}
	if o.TrimStart > 0 {
		body["trim_start"] = o.TrimStart
	}
	if o.TrimEnd > 0 {
		body["trim_end"] = o.TrimEnd
	}
	if o.ThumbCount > 0 {
		body["thumb_count"] = o.ThumbCount
		if o.ThumbSize != "" {
			body["thumb_size"] = o.ThumbSize
		}
	}
	if o.ThumbAt > 0 {
		body["thumb_at"] = o.ThumbAt
		if o.ThumbSize != "" {
			body["thumb_size"] = o.ThumbSize
		}
	}
	if o.SourceServer != 0 {
		body["source_server_id"] = o.SourceServer
	}
	if o.Forced {
		body["forced"] = true
	}
	if o.DefaultTrack {
		body["default"] = true
	}
	return body
}

// jobsAsset fetches a produced asset (e.g. a thumbnail) of a finished job and
// prints it as a base64 data URI. A job's assets are listed by `jobs get`.
func jobsAsset(args []string) error {
	fs := remoteFlagSet("jobs asset")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("jobs asset: <job id> <asset name> required")
	}
	c := newClient(rf)
	var out struct {
		Name string `json:"name"`
		URI  string `json:"uri"`
		Mime string `json:"mime"`
		Data string `json:"data"`
	}
	if err := c.get("/jobs/"+pos[0]+"/assets/"+pos[1], &out); err != nil {
		return err
	}
	fmt.Printf("asset %s  uri=%s  mime=%s\n", out.Name, out.URI, out.Mime)
	fmt.Printf("data:%s;base64,%s\n", out.Mime, out.Data)
	return nil
}

func jobsEvents(args []string) error {
	fs := remoteFlagSet("jobs events")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("jobs events: job id argument is required")
	}
	c := newClient(rf)
	var out struct {
		Events []struct {
			ID        string          `json:"id"`
			Kind      string          `json:"kind"`
			Payload   json.RawMessage `json:"payload"`
			CreatedAt string          `json:"created_at"`
		} `json:"events"`
	}
	if err := c.get("/jobs/"+pos[0]+"/events", &out); err != nil {
		return err
	}
	w := newTable("EVENT", "KIND", "PAYLOAD")
	for _, e := range out.Events {
		w.row(e.ID, e.Kind, string(e.Payload))
	}
	w.print()
	return nil
}

func jobsAction(args []string) error {
	fs := remoteFlagSet("jobs action")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("jobs action: <id> <cancel|retry|pause|resume> required")
	}
	id, action := pos[0], pos[1]
	switch action {
	case "cancel", "retry", "pause", "resume":
	default:
		return fmt.Errorf("jobs action: action must be cancel|retry|pause|resume")
	}
	c := newClient(rf)
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := c.post("/jobs/"+id+"/"+action, map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Printf("job %s -> %s\n", out.ID, out.Status)
	return nil
}

// jobsPriority changes a queued/reserved job's priority so it can jump (or
// drop back in) the queue without being cancelled and resubmitted.
func jobsPriority(args []string) error {
	fs := remoteFlagSet("jobs priority")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("jobs priority: <id> <emergency|high|normal|low|background> required")
	}
	id, priority := pos[0], pos[1]
	c := newClient(rf)
	var out struct {
		ID       string `json:"id"`
		Priority string `json:"priority"`
	}
	if err := c.patch("/jobs/"+id+"/priority", map[string]any{"priority": priority}, &out); err != nil {
		return err
	}
	fmt.Printf("job %s priority -> %s\n", out.ID, out.Priority)
	return nil
}

// jobsLog prints the saved execution log (ffmpeg/whisper stderr) of a task.
func jobsLog(args []string) error {
	fs := remoteFlagSet("jobs log")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("jobs log: <job id> <task id> required")
	}
	c := newClient(rf)
	var out struct {
		TaskID string `json:"task_id"`
		Log    string `json:"log"`
	}
	if err := c.get("/jobs/"+pos[0]+"/tasks/"+pos[1]+"/log", &out); err != nil {
		return err
	}
	if out.Log == "" {
		fmt.Println("(no log captured for this task)")
		return nil
	}
	fmt.Print(out.Log)
	return nil
}

// jobsDelete removes a finished job and all of its data from the server.
func jobsDelete(args []string) error {
	fs := remoteFlagSet("jobs delete")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("jobs delete: job id argument is required")
	}
	c := newClient(rf)
	var out struct {
		ID     string `json:"id"`
		Status string `json:"status"`
	}
	if err := c.del("/jobs/"+pos[0], &out); err != nil {
		return err
	}
	fmt.Printf("job %s deleted\n", out.ID)
	return nil
}
