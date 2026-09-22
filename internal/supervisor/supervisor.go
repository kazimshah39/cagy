package supervisor

import (
	"context"
	"fmt"
	"sort"
	"strings"

	proc "github.com/kazimshah39/herdr-tandem/internal/process"
)

const (
	CodexID    = "codex"
	OpenCodeID = "opencode"
	AgyID      = "agy"
)

type LaunchContext struct {
	ProjectDir   string
	Executable   string
	MCPEnv       map[string]string
	BaseEnv      []string
	Instructions string
	RuntimeID    string
	Model        string
	ConfigRoot   string
}

type LaunchLifecycle interface {
	LaunchArtifact(LaunchContext) (string, error)
	PrepareLaunch(context.Context, proc.Runner, LaunchContext, string) error
	CleanupLaunch(context.Context, proc.Runner, LaunchContext, string) error
}

type LaunchSpec struct {
	Dir  string
	Args []string
	Env  []string
}

type Adapter interface {
	ID() string
	DisplayName() string
	Executable() string
	Validate(context.Context, proc.Runner) error
	BuildLaunch(LaunchContext) (LaunchSpec, error)
}

type Registry struct{ adapters map[string]Adapter }

func NewRegistry(adapters ...Adapter) (Registry, error) {
	registry := Registry{adapters: make(map[string]Adapter, len(adapters))}
	for _, adapter := range adapters {
		if adapter == nil {
			return Registry{}, fmt.Errorf("supervisor adapter is nil")
		}
		id := strings.TrimSpace(adapter.ID())
		if id == "" {
			return Registry{}, fmt.Errorf("supervisor adapter ID is empty")
		}
		if _, exists := registry.adapters[id]; exists {
			return Registry{}, fmt.Errorf("duplicate supervisor adapter %q", id)
		}
		registry.adapters[id] = adapter
	}
	return registry, nil
}

func DefaultRegistry() Registry {
	registry, err := NewRegistry(Codex{}, OpenCode{}, Agy{})
	if err != nil {
		panic(err)
	}
	return registry
}

func (r Registry) Resolve(id string) (Adapter, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		id = CodexID
	}
	adapter, ok := r.adapters[id]
	if !ok {
		return nil, fmt.Errorf("unsupported supervisor %q; choose %s", id, strings.Join(r.IDs(), " or "))
	}
	return adapter, nil
}

func (r Registry) IDs() []string {
	ids := make([]string, 0, len(r.adapters))
	for id := range r.adapters {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func mergeEnv(base []string, values map[string]string) []string {
	merged := make(map[string]string, len(base)+len(values))
	for _, item := range base {
		if key, value, ok := strings.Cut(item, "="); ok {
			merged[key] = value
		}
	}
	for key, value := range values {
		merged[key] = value
	}
	keys := make([]string, 0, len(merged))
	for key := range merged {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+merged[key])
	}
	return out
}
