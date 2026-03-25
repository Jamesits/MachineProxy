// Package envfilter implements environment variable filtering for remote exec.
//
// The C hook library sets MPROXY_CHANGED_ENVS with the names of variables
// that were added or modified after the hook was loaded. These are always
// forwarded. Other variables are filtered through configurable keep/remove
// glob patterns (similar to sudo's env_keep).
package envfilter

import (
	"path/filepath"
	"strings"
)

const changedEnvsKey = "MPROXY_CHANGED_ENVS"

// Filter returns a filtered copy of env suitable for sending to the remote.
//
// Rules (applied in order):
//  1. MPROXY_CHANGED_ENVS is always stripped from the output.
//  2. If a var name matches any remove pattern → drop.
//  3. If a var name is in the changed set (from MPROXY_CHANGED_ENVS) → keep.
//  4. If a var name matches any keep pattern → keep.
//  5. Otherwise → drop.
func Filter(env []string, keep []string, remove []string) []string {
	changed := parseChangedEnvs(env)

	out := make([]string, 0, len(env))
	for _, entry := range env {
		name := envName(entry)

		// Always strip the internal marker.
		if name == changedEnvsKey {
			continue
		}

		if matchesAny(name, remove) {
			continue
		}

		if changed[name] || matchesAny(name, keep) {
			out = append(out, entry)
		}
	}
	return out
}

// parseChangedEnvs extracts the set of changed variable names from
// MPROXY_CHANGED_ENVS in the env slice.
func parseChangedEnvs(env []string) map[string]bool {
	m := make(map[string]bool)
	prefix := changedEnvsKey + "="
	for _, entry := range env {
		if strings.HasPrefix(entry, prefix) {
			val := entry[len(prefix):]
			if val == "" {
				break
			}
			for _, name := range strings.Split(val, ":") {
				if name != "" {
					m[name] = true
				}
			}
			break
		}
	}
	return m
}

// envName returns the variable name (before the '=').
func envName(entry string) string {
	if before, _, ok := strings.Cut(entry, "="); ok {
		return before
	}
	return entry
}

// matchesAny returns true if name matches any of the glob patterns.
func matchesAny(name string, patterns []string) bool {
	for _, p := range patterns {
		if matched, _ := filepath.Match(p, name); matched {
			return true
		}
	}
	return false
}
