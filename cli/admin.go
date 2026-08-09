package cli

import (
	"fmt"
	"strings"
)

// --- keys ---

func cmdKeys(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("keys: missing subcommand (create|list|delete)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "create":
		return keysCreate(rest)
	case "list":
		return keysList(rest)
	case "delete":
		return keysDelete(rest)
	default:
		return fmt.Errorf("keys: unknown subcommand %q (create|list|delete)", sub)
	}
}

func keysCreate(args []string) error {
	fs := remoteFlagSet("keys create")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("keys create: <name> <scope...> required (scopes: jobs:read jobs:write queue:read workers:read webhooks:manage keys:manage ...)")
	}
	c := newClient(rf)
	var out struct {
		ID  string `json:"id"`
		Key string `json:"key"`
	}
	if err := c.post("/keys", map[string]any{
		"name":   pos[0],
		"scopes": pos[1:],
	}, &out); err != nil {
		return err
	}
	fmt.Printf("key %s created\n", out.ID)
	fmt.Printf("  raw key (shown once): %s\n", out.Key)
	return nil
}

func keysList(args []string) error {
	fs := remoteFlagSet("keys list")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Keys []struct {
			ID     string   `json:"id"`
			Name   string   `json:"name"`
			Scopes []string `json:"scopes"`
		} `json:"keys"`
	}
	if err := c.get("/keys", &out); err != nil {
		return err
	}
	if len(out.Keys) == 0 {
		fmt.Println("no keys")
		return nil
	}
	w := newTable("ID", "NAME", "SCOPES")
	for _, k := range out.Keys {
		w.row(k.ID, k.Name, strings.Join(k.Scopes, ","))
	}
	w.print()
	return nil
}

func keysDelete(args []string) error {
	fs := remoteFlagSet("keys delete")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("keys delete: key id argument is required")
	}
	c := newClient(rf)
	if err := c.del("/keys/"+pos[0], nil); err != nil {
		return err
	}
	fmt.Printf("key %s deleted\n", pos[0])
	return nil
}

// --- webhooks ---

func cmdWebhooks(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("webhooks: missing subcommand (list|create|delete|replay)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return webhooksList(rest)
	case "create":
		return webhooksCreate(rest)
	case "delete":
		return webhooksDelete(rest)
	case "replay":
		return webhooksReplay(rest)
	default:
		return fmt.Errorf("webhooks: unknown subcommand %q (list|create|delete|replay)", sub)
	}
}

func webhooksList(args []string) error {
	fs := remoteFlagSet("webhooks list")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Webhooks []struct {
			ID     string   `json:"id"`
			URL    string   `json:"url"`
			Events []string `json:"events"`
		} `json:"webhooks"`
	}
	if err := c.get("/webhooks", &out); err != nil {
		return err
	}
	if len(out.Webhooks) == 0 {
		fmt.Println("no webhooks")
		return nil
	}
	w := newTable("ID", "URL", "EVENTS")
	for _, h := range out.Webhooks {
		w.row(h.ID, h.URL, strings.Join(h.Events, ","))
	}
	w.print()
	return nil
}

func webhooksCreate(args []string) error {
	fs := remoteFlagSet("webhooks create")
	secret := fs.String("secret", "", "HMAC signing secret")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("webhooks create: <url> <event...> required (events: job.created job.started job.completed job.failed plugin.finished ... or *)")
	}
	c := newClient(rf)
	var out struct {
		ID string `json:"id"`
	}
	if err := c.post("/webhooks", map[string]any{
		"url":    pos[0],
		"events": pos[1:],
		"secret": *secret,
	}, &out); err != nil {
		return err
	}
	fmt.Printf("webhook %s created\n", out.ID)
	return nil
}

func webhooksDelete(args []string) error {
	fs := remoteFlagSet("webhooks delete")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("webhooks delete: webhook id argument is required")
	}
	c := newClient(rf)
	if err := c.del("/webhooks/"+pos[0], nil); err != nil {
		return err
	}
	fmt.Printf("webhook %s deleted\n", pos[0])
	return nil
}

func webhooksReplay(args []string) error {
	fs := remoteFlagSet("webhooks replay")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("webhooks replay: event id argument is required")
	}
	c := newClient(rf)
	var out struct {
		EventID  string `json:"event_id"`
		Replayed int    `json:"replayed"`
	}
	if err := c.post("/webhooks/"+pos[0]+"/replay", map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Printf("event %s: %d dead-lettered delivery(s) reset to pending\n", out.EventID, out.Replayed)
	return nil
}

// --- storage servers ---

func cmdStorage(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("storage: missing subcommand (list|add|rebuild)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return storageList(rest)
	case "add":
		return storageAdd(rest)
	case "rebuild":
		return storageRebuild(rest)
	default:
		return fmt.Errorf("storage: unknown subcommand %q (list|add|rebuild)", sub)
	}
}

func storageList(args []string) error {
	fs := remoteFlagSet("storage list")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Servers []struct {
			ID   int    `json:"id"`
			Type string `json:"type"`
			Host string `json:"host"`
			User string `json:"user"`
		} `json:"servers"`
	}
	if err := c.get("/storage/servers", &out); err != nil {
		return err
	}
	if len(out.Servers) == 0 {
		fmt.Println("no storage servers (destination 0 = default local)")
		return nil
	}
	w := newTable("ID", "TYPE", "HOST", "USER")
	for _, s := range out.Servers {
		w.row(fmt.Sprintf("%d", s.ID), s.Type, s.Host, s.User)
	}
	w.print()
	return nil
}

func storageAdd(args []string) error {
	fs := remoteFlagSet("storage add")
	host := fs.String("host", "", "host (ssh host or s3/minio/r2 endpoint)")
	user := fs.String("user", "", "ssh user (default weft)")
	keyPath := fs.String("key-path", "", "ssh private key path (or --password for password auth)")
	basePath := fs.String("base-path", "", "remote/local base dir")
	port := fs.Int("port", 0, "ssh port (default 22)")
	password := fs.String("password", "", "ssh password (alternative to --key-path)")
	bucket := fs.String("bucket", "", "s3/minio/r2 bucket")
	region := fs.String("region", "us-east-1", "s3 region")
	accessKey := fs.String("access-key", "", "s3/minio/r2 access key")
	secretKey := fs.String("secret-key", "", "s3/minio/r2 secret key")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) < 2 {
		return fmt.Errorf("storage add: <id> <type> required (type: ssh|local|s3|minio|r2)")
	}
	var id int
	if _, err := fmt.Sscanf(pos[0], "%d", &id); err != nil || id == 0 {
		return fmt.Errorf("storage add: id must be a non-zero integer")
	}
	c := newClient(rf)
	config := map[string]any{}
	if *keyPath != "" {
		config["key_path"] = *keyPath
	}
	if *password != "" {
		config["password"] = *password
	}
	if *basePath != "" {
		config["base_path"] = *basePath
	}
	if *port != 0 {
		config["port"] = *port
	}
	if *bucket != "" {
		config["bucket"] = *bucket
	}
	config["region"] = *region
	if *accessKey != "" {
		config["access_key"] = *accessKey
	}
	if *secretKey != "" {
		config["secret_key"] = *secretKey
	}
	var out struct {
		ID     int    `json:"id"`
		Status string `json:"status"`
	}
	if err := c.post("/storage/servers", map[string]any{
		"id":     id,
		"type":   pos[1],
		"host":   *host,
		"user":   *user,
		"config": config,
	}, &out); err != nil {
		return err
	}
	fmt.Printf("storage server %d (%s) registered\n", out.ID, pos[1])
	return nil
}

// storageRebuild scans a destination storage directory and regenerates its
// master playlist from the files actually present.
func storageRebuild(args []string) error {
	fs := remoteFlagSet("storage rebuild")
	destination := fs.Int("destination", 0, "destination id (0 = default local)")
	path := fs.String("path", "", "subdirectory under the storage root (e.g. movie1)")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	if *path == "" {
		return fmt.Errorf("storage rebuild: --path is required (e.g. --path Series-Test/movie1)")
	}
	c := newClient(rf)
	var out struct {
		Status     string `json:"status"`
		Renditions []string
		Subtitles  []struct {
			Lang string `json:"lang"`
			URI  string `json:"uri"`
		}
		Audios []struct {
			Lang string `json:"lang"`
			URI  string `json:"uri"`
		}
		Master string `json:"master"`
	}
	if err := c.post("/storage/rebuild-master", map[string]any{
		"destination_id": *destination,
		"path":           *path,
	}, &out); err != nil {
		return err
	}
	fmt.Printf("rebuilt master at %s (destination %d)\n", *path, *destination)
	fmt.Printf("renditions: %s\n", strings.Join(out.Renditions, ", "))
	if len(out.Subtitles) == 0 && len(out.Audios) == 0 {
		fmt.Println("no subtitle/audio tracks found")
	}
	for _, s := range out.Subtitles {
		fmt.Printf("subtitle [%s] %s\n", s.Lang, s.URI)
	}
	for _, a := range out.Audios {
		fmt.Printf("audio     [%s] %s\n", a.Lang, a.URI)
	}
	return nil
}

// --- queue / workers / profiles / plugins / metrics / benchmark ---

func cmdQueue(args []string) error {
	fs := remoteFlagSet("queue")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Queue map[string]int `json:"queue"`
	}
	if err := c.get("/queue", &out); err != nil {
		return err
	}
	w := newTable("PRIORITY", "COUNT")
	for k, v := range out.Queue {
		w.row(k, fmt.Sprintf("%d", v))
	}
	w.print()
	return nil
}

func cmdWorkers(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "scale":
			return workersScale(args[1:])
		}
	}
	fs := remoteFlagSet("workers")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Workers []struct {
			ID            string `json:"id"`
			Status        string `json:"status"`
			CurrentTaskID string `json:"current_task_id"`
		} `json:"workers"`
	}
	if err := c.get("/workers", &out); err != nil {
		return err
	}
	w := newTable("ID", "STATUS", "TASK")
	for _, wk := range out.Workers {
		w.row(wk.ID, wk.Status, wk.CurrentTaskID)
	}
	w.print()
	return nil
}

// workersScale resizes the worker pool at runtime without a daemon restart.
func workersScale(args []string) error {
	fs := remoteFlagSet("workers scale")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("workers scale: <count> argument is required")
	}
	var n int
	if _, err := fmt.Sscanf(pos[0], "%d", &n); err != nil || n < 1 {
		return fmt.Errorf("workers scale: count must be an integer >= 1")
	}
	c := newClient(rf)
	var out struct {
		Workers int `json:"workers"`
	}
	if err := c.post("/workers/scale", map[string]any{"count": n}, &out); err != nil {
		return err
	}
	fmt.Printf("worker pool scaled to %d\n", out.Workers)
	return nil
}

func cmdProfiles(args []string) error {
	fs := remoteFlagSet("profiles")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Profiles []struct {
			Name        string `json:"name"`
			Description string `json:"description"`
		} `json:"profiles"`
	}
	if err := c.get("/profiles", &out); err != nil {
		return err
	}
	w := newTable("NAME", "DESCRIPTION")
	for _, p := range out.Profiles {
		w.row(p.Name, p.Description)
	}
	w.print()
	return nil
}

func cmdPlugins(args []string) error {
	fs := remoteFlagSet("plugins")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Plugins []struct {
			Name  string   `json:"name"`
			Kinds []string `json:"kinds"`
		} `json:"plugins"`
	}
	if err := c.get("/plugins", &out); err != nil {
		return err
	}
	w := newTable("NAME", "KINDS")
	for _, p := range out.Plugins {
		w.row(p.Name, strings.Join(p.Kinds, ","))
	}
	w.print()
	return nil
}

func cmdMetrics(args []string) error {
	fs := remoteFlagSet("metrics")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var raw []byte
	if err := c.getText("/metrics", &raw); err != nil {
		return err
	}
	fmt.Print(string(raw))
	return nil
}

// cmdBenchmark defaults to triggering a fresh run (POST /benchmark), matching
// existing behavior. "get"/"show" fetches the last recorded result (GET
// /benchmark) instead of running a new one.
func cmdBenchmark(args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "get", "show":
			return benchmarkGet(args[1:])
		}
	}
	fs := remoteFlagSet("benchmark")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		CPUScore    float64 `json:"cpu_score"`
		FFmpegScore float64 `json:"ffmpeg_score"`
	}
	if err := c.post("/benchmark", map[string]any{}, &out); err != nil {
		return err
	}
	fmt.Printf("benchmark done — cpu_score=%.2f ffmpeg_score=%.2f\n", out.CPUScore, out.FFmpegScore)
	return nil
}

func benchmarkGet(args []string) error {
	fs := remoteFlagSet("benchmark get")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		CPUScore    float64 `json:"cpu_score"`
		FFmpegScore float64 `json:"ffmpeg_score"`
		DiskIOScore float64 `json:"disk_io_score"`
		MemoryScore float64 `json:"memory_score"`
		CreatedAt   string  `json:"created_at"`
	}
	if err := c.get("/benchmark", &out); err != nil {
		return err
	}
	fmt.Printf("last benchmark (%s):\n", out.CreatedAt)
	fmt.Printf("  cpu_score=%.2f ffmpeg_score=%.2f disk_io_score=%.2f memory_score=%.2f\n",
		out.CPUScore, out.FFmpegScore, out.DiskIOScore, out.MemoryScore)
	return nil
}

func cmdSystem(args []string) error {
	fs := remoteFlagSet("system")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		NumCPU     int     `json:"num_cpu"`
		Load1      float64 `json:"load1"`
		Load5      float64 `json:"load5"`
		Load15     float64 `json:"load15"`
		CPUPercent float64 `json:"cpu_percent"`
		MemTotal   uint64  `json:"mem_total"`
		MemUsed    uint64  `json:"mem_used"`
		MemAvail   uint64  `json:"mem_avail"`
		MemPct     float64 `json:"mem_percent"`
		DiskTotal  uint64  `json:"disk_total"`
		DiskUsed   uint64  `json:"disk_used"`
		DiskAvail  uint64  `json:"disk_avail"`
		DiskPct    float64 `json:"disk_percent"`
		Hostname   string  `json:"hostname"`
		Uptime     int64   `json:"uptime_seconds"`
	}
	if err := c.get("/system", &out); err != nil {
		return err
	}
	fmt.Printf("host:       %s\n", out.Hostname)
	fmt.Printf("cpu:        %d cores, load %.2f/%.2f/%.2f (%.1f%%)\n", out.NumCPU, out.Load1, out.Load5, out.Load15, out.CPUPercent)
	fmt.Printf("memory:     %s used / %s total (%.1f%%)\n", bytesHuman(out.MemUsed), bytesHuman(out.MemTotal), out.MemPct)
	fmt.Printf("disk:       %s used / %s total (%.1f%%)\n", bytesHuman(out.DiskUsed), bytesHuman(out.DiskTotal), out.DiskPct)
	up := out.Uptime
	fmt.Printf("uptime:     %dd %dh %dm\n", up/86400, up%86400/3600, up%3600/60)
	return nil
}

func bytesHuman(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}
