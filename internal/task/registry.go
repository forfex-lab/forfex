package task

import (
	"context"
	"fmt"
	"sort"
	"sync"

	"github.com/forfex-lab/forfex/internal/bus"
	"github.com/forfex-lab/forfex/internal/mcp"
)

// Registry is a catalogue of tasks, keyed by name.
//
// It refuses to replace an existing entry, for the reason bus.Register
// and mcp.Registry.Add refuse it: a silent replacement could change
// where a name sends its arguments, or relabel the provenance of an
// input, without anything in the code that calls it changing at all.
type Registry struct {
	mu    sync.RWMutex
	tasks map[string]Task
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{tasks: make(map[string]Task)}
}

// Register validates the task and adds it.
func (r *Registry) Register(t Task) error {
	if err := t.Validate(); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.tasks[t.Name]; exists {
		return fmt.Errorf("task: %q is already registered", t.Name)
	}
	r.tasks[t.Name] = t
	return nil
}

// MustRegister is Register for a package-level catalogue, where a
// malformed definition is a programming error and should stop the
// binary rather than surface on the first call.
func (r *Registry) MustRegister(ts ...Task) {
	for _, t := range ts {
		if err := r.Register(t); err != nil {
			panic(err)
		}
	}
}

// Lookup returns a task by name.
func (r *Registry) Lookup(name string) (Task, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	t, ok := r.tasks[name]
	return t, ok
}

// Names returns every registered task name, sorted.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tasks))
	for n := range r.tasks {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// All returns every registered task, ordered by name.
func (r *Registry) All() []Task {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Task, 0, len(r.tasks))
	for _, t := range r.tasks {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Run looks a task up and runs it.
func (r *Registry) Run(ctx context.Context, b *bus.Bus, name string, args map[string]any) (mcp.ToolResult, error) {
	t, ok := r.Lookup(name)
	if !ok {
		return mcp.ToolResult{}, fmt.Errorf("%w: %q", ErrUnknownTask, name)
	}
	return Run(ctx, b, t, args)
}
