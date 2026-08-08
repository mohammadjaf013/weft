package cli

import (
	"encoding/json"
	"fmt"
)

// cmdJobs handles `weft jobs ...`: list, get, create, events, and action.
func cmdJobs(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("jobs: missing subcommand (list|get|create|events|action)")
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
	case "action":
		return jobsAction(rest)
	default:
		return fmt.Errorf("jobs: unknown subcommand %q (list|get|create|events|action)", sub)
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
	body := jobsCreateBody(pos[0], *profile, *priority, *dest, *lang, *srcLang, *name, *path, *deleteSource, *provider)
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

// jobsCreateBody builds the /jobs payload from CLI flags (separate so the flag
// plumbing is unit-testable without an HTTP round trip).
func jobsCreateBody(inputRef, profile, priority string, dest int, lang, srcLang, name, path string, deleteSource bool, provider string) map[string]any {
	body := map[string]any{
		"input_ref":      inputRef,
		"profile":        profile,
		"priority":       priority,
		"destination_id": dest,
	}
	if lang != "" {
		body["lang"] = lang
	}
	if srcLang != "" {
		body["src_lang"] = srcLang
	}
	if name != "" {
		body["name"] = name
	}
	if path != "" {
		body["path"] = path
	}
	if deleteSource {
		body["delete_source"] = true
	}
	if provider != "" {
		body["provider"] = provider
	}
	return body
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
