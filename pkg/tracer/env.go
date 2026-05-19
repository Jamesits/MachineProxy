//go:build linux

package tracer

import (
	"strings"
)

// EnvBaseline holds a snapshot of the environment for computing diffs.
type EnvBaseline struct {
	entries map[string]string // key → "KEY=VALUE"
}

// NewEnvBaselineFromSlice creates a baseline from a string slice (e.g. os.Environ()).
func NewEnvBaselineFromSlice(env []string) *EnvBaseline {
	b := &EnvBaseline{entries: make(map[string]string)}
	for _, entry := range env {
		if idx := strings.IndexByte(entry, '='); idx >= 0 {
			b.entries[entry[:idx]] = entry
		}
	}
	return b
}

// skipKeys are internal vars we never report as changed.
var skipKeys = map[string]bool{
	"MPROXY_CHANGED_ENVS": true,
	"MPROXY_HOOK_BYPASS":  true,
}

// ChangedKeys computes which env var names in current are new or differ from
// the baseline. Returns the colon-separated string for MPROXY_CHANGED_ENVS,
// or empty string if nothing changed.
func (b *EnvBaseline) ChangedKeys(current []string) string {
	var changed []string
	for _, entry := range current {
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		key := entry[:idx]
		if skipKeys[key] {
			continue
		}
		if snap, ok := b.entries[key]; !ok || snap != entry {
			changed = append(changed, key)
		}
	}
	return strings.Join(changed, ":")
}

// InjectEnvVars appends or replaces MPROXY_HOOK_BYPASS=1 and
// MPROXY_CHANGED_ENVS=... in the given envp slice.
func (b *EnvBaseline) InjectEnvVars(envp []string) []string {
	changedKeys := b.ChangedKeys(envp)

	// Remove existing MPROXY_ control vars.
	var result []string
	for _, e := range envp {
		if strings.HasPrefix(e, "MPROXY_HOOK_BYPASS=") ||
			strings.HasPrefix(e, "MPROXY_CHANGED_ENVS=") {
			continue
		}
		result = append(result, e)
	}

	result = append(result, "MPROXY_HOOK_BYPASS=1")
	if changedKeys != "" {
		result = append(result, "MPROXY_CHANGED_ENVS="+changedKeys)
	}
	return result
}
