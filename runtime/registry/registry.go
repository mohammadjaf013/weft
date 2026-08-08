// Package registry loads plugins and sandboxes every Process() call. A
// panicking plugin fails only its own Task — it can never crash the Agent.
package registry

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/mohammadjaf013/weft/core"
)

// Registry maps task kinds to the plugin that handles them, and wraps every
// Process() call in recover(). It is the only place plugins are looked up.
type Registry struct {
	mu      sync.RWMutex
	byKind  map[string]core.Plugin
	plugins map[string]core.Plugin // by Name()
}

func New() *Registry {
	return &Registry{
		byKind:  map[string]core.Plugin{},
		plugins: map[string]core.Plugin{},
	}
}

// Register adds a plugin and indexes it under each kind it declares.
func (r *Registry) Register(p core.Plugin) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.plugins[p.Name()]; exists {
		return fmt.Errorf("plugin %q already registered", p.Name())
	}
	for _, k := range p.Capabilities().SupportedKinds {
		if _, clash := r.byKind[k]; clash {
			return fmt.Errorf("task kind %q already claimed by another plugin", k)
		}
		r.byKind[k] = p
	}
	r.plugins[p.Name()] = p
	return nil
}

func (r *Registry) HasPlugin(name string) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, ok := r.plugins[name]
	return ok
}

func (r *Registry) PluginFor(kind string) (core.Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.byKind[kind]
	return p, ok
}

func (r *Registry) PluginByName(name string) (core.Plugin, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	p, ok := r.plugins[name]
	return p, ok
}

func (r *Registry) List() []core.Plugin {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]core.Plugin, 0, len(r.plugins))
	for _, p := range r.plugins {
		out = append(out, p)
	}
	return out
}

var (
	ErrNoPluginForKind = errors.New("no plugin registered for task kind")
	ErrPluginPanicked  = errors.New("plugin panicked")
)

// Process invokes a plugin's Process for the given task kind inside a
// recover() sandbox. On panic it returns ErrPluginPanicked so the caller can
// fail only that task.
func (r *Registry) Process(ctx context.Context, kind string, in core.TaskInput) (out core.TaskOutput, err error) {
	p, ok := r.PluginFor(kind)
	if !ok {
		return out, fmt.Errorf("%w: %s", ErrNoPluginForKind, kind)
	}

	type result struct {
		out core.TaskOutput
		err error
	}
	ch := make(chan result, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				ch <- result{err: fmt.Errorf("%w: %v", ErrPluginPanicked, r)}
			}
		}()
		o, e := p.Process(ctx, in)
		ch <- result{out: o, err: e}
	}()

	res := <-ch
	return res.out, res.err
}
