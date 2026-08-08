// Package daemon wires the Weft runtime together from a Config and runs it.
// This is the only place every layer (Core, Runtime, Plugins, API, Webhooks)
// is assembled. The CLI's `serve` command calls Daemon.Serve.
package daemon

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	cfg "github.com/mohammadjaf013/weft/configs"
	"github.com/mohammadjaf013/weft/core"
	"github.com/mohammadjaf013/weft/plugins/aisubtitle"
	"github.com/mohammadjaf013/weft/plugins/audioenc"
	"github.com/mohammadjaf013/weft/plugins/hls"
	"github.com/mohammadjaf013/weft/plugins/masterplaylist"
	"github.com/mohammadjaf013/weft/plugins/masterupdate"
	"github.com/mohammadjaf013/weft/plugins/subtitle"
	"github.com/mohammadjaf013/weft/plugins/thumbnail"
	"github.com/mohammadjaf013/weft/plugins/upload"
	"github.com/mohammadjaf013/weft/plugins/videoenc"
	"github.com/mohammadjaf013/weft/runtime/api"
	ffexec "github.com/mohammadjaf013/weft/runtime/executor/ffmpeg"
	"github.com/mohammadjaf013/weft/runtime/metrics"
	"github.com/mohammadjaf013/weft/runtime/registry"
	"github.com/mohammadjaf013/weft/runtime/store/sqlite"
	"github.com/mohammadjaf013/weft/runtime/webhook"
	"github.com/mohammadjaf013/weft/runtime/worker"
)

// Daemon owns every long-lived component. Built via New, started via Serve.
type Daemon struct {
	cfg    *cfg.Config
	store  *sqlite.Store
	bus    core.EventBus
	sched  core.DAGScheduler
	sm     core.StateMachine
	reg    *registry.Registry
	exec   core.Executor
	metric *metrics.Metrics
	km     *api.KeyManager
	srv    *api.Server
	wh     *webhook.Dispatcher

	workers []*worker.Worker
	pool    *workerPool

	httpSrv *http.Server
	cancel  context.CancelFunc
	wg      sync.WaitGroup

	// inputResolver overrides the default local-path InputRef resolver.
	// nil means "use the daemon default".
	inputResolver func(core.Job) (string, error)

	// Addr is the bound listener address after Serve starts (incl. ephemeral
	// ports when Network.Listen is ":0").
	Addr string
}

// Options lets embedders override parts of the daemon. Zero values use the
// production defaults. Tests use it to inject a fake executor and stub plugins.
type Options struct {
	// Executor overrides the default ffmpeg executor. Needed by e2e tests so no
	// real binary is required.
	Executor core.Executor
	// Registry pre-populated by the caller. When set, the default plugin set is
	// replaced entirely (an empty registry is used).
	Registry *registry.Registry
	// PluginRegister, when non-nil, is called instead of the default plugin set.
	PluginRegister func(reg *registry.Registry, c *cfg.Config) error
	// InputResolver maps a job's InputRef to a local InputURI. When nil, the
	// daemon uses its default resolver (local paths; remote schemes error).
	InputResolver func(job core.Job) (string, error)
}

// Open assembles all components from a config (and optionally a pre-opened
// store). It does not start network listeners.
func Open(c *cfg.Config, store *sqlite.Store, opts ...Options) (*Daemon, error) {
	o := Options{}
	if len(opts) > 0 {
		o = opts[0]
	}
	if store == nil {
		s, err := sqlite.Open(c.Database.Path)
		if err != nil {
			return nil, fmt.Errorf("open db %s: %w", c.Database.Path, err)
		}
		store = s
	}
	bus := core.NewEventBus()
	sm := core.NewStateMachine(store, bus)
	sched := core.NewDAGScheduler(store, bus, sm)
	reg := o.Registry
	if reg == nil {
		reg = registry.New()
	}
	exec := o.Executor
	if exec == nil {
		exec = ffexec.New(ffexec.CommandLocator{FFmpeg: c.Executor.FFmpegPath, FFprobe: c.Executor.FFprobePath})
	}
	m := metrics.New(store)

	regFn := o.PluginRegister
	if regFn == nil {
		regFn = registerPlugins
	}
	if err := regFn(reg, c); err != nil {
		return nil, err
	}

	adminKey := c.Security.AdminAPIKey
	km := api.NewKeyManager(store, adminKey)

	pool := newWorkerPool()
	d := &Daemon{
		cfg: c, store: store, bus: bus, sched: sched, sm: sm,
		reg: reg, exec: exec, metric: m, km: km, pool: pool,
		inputResolver: o.InputResolver,
	}

	// API server
	d.srv = api.NewServer(api.Options{
		Store:    store,
		Bus:      bus,
		Sched:    sched,
		SM:       sm,
		Registry: reg,
		Metrics:  m,
		Config:   c,
		Keys:     km,
		Worker:   pool,
		Storage:  d.storageForID,
	})
	return d, nil
}

// registerPlugins enables the media plugins listed in config. The AI-subtitle
// plugin is only enabled when a provider is configured (whisper/gemini).
func registerPlugins(reg *registry.Registry, c *cfg.Config) error {
	enabled := map[string]bool{}
	for _, n := range c.Plugins.Enabled {
		enabled[n] = true
	}
	media := map[string]core.Plugin{
		"ffmpeg-video":    &videoenc.Plugin{},
		"ffmpeg-audio":    &audioenc.Plugin{},
		"subtitle":        &subtitle.Plugin{},
		"thumbnail":       &thumbnail.Plugin{},
		"hls":             &hls.Plugin{},
		"master_playlist": &masterplaylist.Plugin{},
		"master_update":   &masterupdate.Plugin{},
		"upload":          &upload.Plugin{},
	}
	for name, p := range media {
		if enabled[name] {
			if err := reg.Register(p); err != nil {
				return fmt.Errorf("register %s: %w", name, err)
			}
		}
	}
	if enabled["ai-subtitle"] {
		prov := aisubtitleProvider(c)
		if prov.Cfg.Provider != "" {
			if err := reg.Register(prov); err != nil {
				return fmt.Errorf("register ai-subtitle: %w", err)
			}
		}
	}
	return nil
}

func aisubtitleProvider(c *cfg.Config) *aisubtitle.Plugin {
	p := &aisubtitle.Plugin{}
	p.Cfg.Provider = c.AI.Provider
	p.Cfg.Whisper.BinPath = c.AI.Whisper.BinPath
	if p.Cfg.Whisper.BinPath == "" {
		p.Cfg.Whisper.BinPath = "whisper-cli"
	}
	p.Cfg.Whisper.ModelPath = c.AI.Whisper.ModelPath
	p.Cfg.Whisper.Language = c.AI.Whisper.Language
	p.Cfg.Whisper.Threads = c.AI.Whisper.Threads
	p.Cfg.Whisper.Temperature = c.AI.Whisper.Temperature
	p.Cfg.Whisper.Prompt = c.AI.Whisper.Prompt
	p.Cfg.Gemini.APIKey = c.AI.Gemini.APIKey
	p.Cfg.Gemini.Model = c.AI.Gemini.Model
	return p
}
