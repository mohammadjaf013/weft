// Package cli implements the weft subcommands. Only `serve` runs the daemon;
// the rest are thin clients over the HTTP API.
package cli

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/daemon"
)

// Run dispatches a subcommand. Global flags (--api/--key/--config) may appear
// either before or after the subcommand; they are hoisted to after it so the
// per-command flag parser can see them.
func Run(args []string) error {
	if len(args) == 0 {
		usage()
		return nil
	}
	args = hoistGlobalFlags(args)
	switch args[0] {
	case "serve":
		return cmdServe(args[1:])
	case "version", "--version", "-v":
		fmt.Printf("weft %s\n", core.Version)
		return nil
	case "gen-config", "init-config":
		return cmdGenConfig(args[1:])
	case "doctor":
		return cmdDoctor(args[1:])
	case "jobs":
		return cmdJobs(args[1:])
	case "keys":
		return cmdKeys(args[1:])
	case "webhooks":
		return cmdWebhooks(args[1:])
	case "storage":
		return cmdStorage(args[1:])
	case "queue":
		return cmdQueue(args[1:])
	case "workers":
		return cmdWorkers(args[1:])
	case "profiles":
		return cmdProfiles(args[1:])
	case "plugins":
		return cmdPlugins(args[1:])
	case "metrics":
		return cmdMetrics(args[1:])
	case "benchmark":
		return cmdBenchmark(args[1:])
	case "system":
		return cmdSystem(args[1:])
	case "config":
		return cmdConfig(args[1:])
	case "cron":
		return cmdCron(args[1:])
	case "dashboard":
		return cmdDashboard(args[1:])
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// hoistGlobalFlags moves --api/--key/--config (with their values) from the
// front of args to after the first positional token, so `weft --key X jobs …`
// works exactly like `weft jobs … --key X`.
func hoistGlobalFlags(args []string) []string {
	var head, tail []string
	i := 0
	for i < len(args) {
		a := args[i]
		name := strings.TrimLeft(a, "-")
		if i := strings.IndexByte(name, '='); i >= 0 {
			name = name[:i]
		}
		switch name {
		case "api", "key", "config":
			tail = append(tail, a)
			if !strings.Contains(a, "=") && i+1 < len(args) {
				tail = append(tail, args[i+1])
				i++
			}
		default:
			head = append(head, args[i])
		}
		i++
	}
	return append(head, tail...)
}

func usage() {
	fmt.Printf(`weft %s — media processing agent

Usage:
  weft serve [--config <path>]   run the agent daemon
  weft init-config [<path>]      write a default weft.yaml (default ./weft.yaml)
  weft doctor [--config <path>]  check the environment (ffmpeg, config, db, …)
  weft version                   print version

API commands (thin clients; defaults from weft.yaml network.listen + security.admin_api_key):
  weft jobs list [--status S] [--priority P] [--limit N]
  weft jobs get <id>
  weft jobs create <input_ref> --profile <name> [--priority P] [--destination N] [--source-server N]
                   [--trim-start S] [--trim-end S] [--thumb-count N|--thumb-at S] [--thumb-size WxH|original]
                   [--forced] [--default] (profiles incl. trim-update, poster-replace — see 'weft profiles')
  weft jobs events <id>
  weft jobs action <id> <cancel|retry|pause|resume>
  weft jobs priority <id> <emergency|high|normal|low|background>
  weft keys create <name> <scope...> | keys list | keys delete <id>
  weft webhooks create <url> <event...> [--secret S] | webhooks list | webhooks delete <id> | webhooks replay <event_id>
  weft storage list | storage add <id> <type> [--host H] [--user U]
  weft queue | workers | profiles | plugins | metrics | benchmark [get|show] | system
  weft config export [--out F] [--include-secrets] | config import <file>
  weft cron list | cron run <cleanup|benchmark|health_scan>
  weft dashboard [--interval 2s]   live view: jobs/queue/workers/system, select+cancel/pause/resume/delete
  (all accept --api <url> and --key <token>)
`, core.Version)
}

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	configPath := fs.String("config", "weft.yaml", "path to config file")
	_ = fs.Parse(args)

	c, err := cfg.Load(*configPath)
	if err != nil {
		if *configPath == "weft.yaml" && os.IsNotExist(err) {
			return fmt.Errorf("no weft.yaml found; run `weft init-config` to create one")
		}
		return err
	}
	if probs := c.Validate(); len(probs) > 0 {
		for _, p := range probs {
			fmt.Fprintf(os.Stderr, "config error: %s\n", p)
		}
		return fmt.Errorf("invalid configuration")
	}

	d, err := daemon.Open(c, nil, daemon.Options{ConfigPath: *configPath})
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return d.Serve(ctx)
}

func cmdGenConfig(args []string) error {
	path := "./weft.yaml"
	if len(args) > 0 {
		path = args[0]
	}
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("%s already exists", path)
	}
	c := cfg.Default()
	if err := writeYAML(path, c); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", path)
	return nil
}
