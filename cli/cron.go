package cli

import (
	"fmt"
)

// cmdCron handles `weft cron list|run <job>`.
func cmdCron(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("cron: missing subcommand (list|run)")
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "list":
		return cronList(rest)
	case "run":
		return cronRun(rest)
	default:
		return fmt.Errorf("cron: unknown subcommand %q (list|run)", sub)
	}
}

func cronList(args []string) error {
	fs := remoteFlagSet("cron list")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	c := newClient(rf)
	var out struct {
		Jobs []struct {
			Name     string `json:"name"`
			Schedule string `json:"schedule"`
			LastRun  string `json:"last_run,omitempty"`
			NextRun  string `json:"next_run,omitempty"`
			LastErr  string `json:"last_error,omitempty"`
		} `json:"jobs"`
	}
	if err := c.get("/cron", &out); err != nil {
		return err
	}
	if len(out.Jobs) == 0 {
		fmt.Println("no cron jobs configured")
		return nil
	}
	w := newTable("NAME", "SCHEDULE", "LAST RUN", "NEXT RUN", "LAST ERROR")
	for _, j := range out.Jobs {
		w.row(j.Name, j.Schedule, orDash(j.LastRun), orDash(j.NextRun), orDash(j.LastErr))
	}
	w.print()
	return nil
}

func cronRun(args []string) error {
	fs := remoteFlagSet("cron run")
	rf, err := parseRemote(fs, args)
	if err != nil {
		return err
	}
	pos := fs.Args()
	if len(pos) == 0 {
		return fmt.Errorf("cron run: <job> required (cleanup|benchmark|health_scan)")
	}
	c := newClient(rf)
	var out struct {
		Job    string `json:"job"`
		Status string `json:"status"`
		Error  string `json:"error,omitempty"`
	}
	if err := c.post("/cron/"+pos[0]+"/run", map[string]any{}, &out); err != nil {
		return err
	}
	if out.Status == "failed" {
		return fmt.Errorf("cron job %s failed: %s", out.Job, out.Error)
	}
	fmt.Printf("cron job %s: %s\n", out.Job, out.Status)
	return nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
