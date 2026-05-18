// Package envfilter implements environment variable filtering for remote exec.
//
// The C hook library sets MPROXY_CHANGED_ENVS with the names of variables
// that were added or modified after the hook was loaded. These are always
// forwarded. Other variables are filtered through configurable keep/remove
// patterns (similar to sudo's env_keep).
//
// Patterns are either globs (filepath.Match syntax) or regexes delimited
// by slashes (e.g. /^AWS_/).
package envfilter

import (
	"path/filepath"
	"regexp"
	"strings"
)

const changedEnvsKey = "MPROXY_CHANGED_ENVS"

// matcher is a compiled pattern that can test env var names.
type matcher interface {
	Match(name string) bool
}

type globMatcher string

func (g globMatcher) Match(name string) bool {
	matched, _ := filepath.Match(string(g), name)
	return matched
}

type regexMatcher struct {
	re *regexp.Regexp
}

func (r *regexMatcher) Match(name string) bool {
	return r.re.MatchString(name)
}

// compileMatcher parses a single pattern string. Patterns enclosed in
// slashes (e.g. /^AWS_/) are compiled as regexes; everything else is
// treated as a glob.
func compileMatcher(pattern string) (matcher, error) {
	if strings.HasPrefix(pattern, "/") && strings.HasSuffix(pattern, "/") && len(pattern) > 2 {
		expr := pattern[1 : len(pattern)-1]
		re, err := regexp.Compile(expr)
		if err != nil {
			return nil, err
		}
		return &regexMatcher{re: re}, nil
	}
	return globMatcher(pattern), nil
}

// compileAll compiles a slice of pattern strings into matchers.
// Invalid regex patterns are silently skipped.
func compileAll(patterns []string) []matcher {
	out := make([]matcher, 0, len(patterns))
	for _, p := range patterns {
		m, err := compileMatcher(p)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out
}

// matchesAnyCompiled returns true if name matches any compiled matcher.
func matchesAnyCompiled(name string, matchers []matcher) bool {
	for _, m := range matchers {
		if m.Match(name) {
			return true
		}
	}
	return false
}

// Filter returns a filtered copy of env suitable for sending to the remote.
//
// Rules (applied in order):
//  1. MPROXY_CHANGED_ENVS is always stripped from the output.
//  2. If a var name matches any remove pattern → drop.
//  3. If a var name is in the changed set (from MPROXY_CHANGED_ENVS) → keep.
//  4. If a var name matches any keep pattern → keep.
//  5. Otherwise → drop.
func Filter(env []string, keep []string, remove []string) []string {
	keepM := compileAll(keep)
	removeM := compileAll(remove)
	return filterCompiled(env, keepM, removeM)
}

// Remove returns a copy of env with variables matching any remove pattern
// stripped. Unlike Filter, there is no keep list — all non-matching vars
// are preserved.
func Remove(env []string, remove []string) []string {
	removeM := compileAll(remove)
	out := make([]string, 0, len(env))
	for _, entry := range env {
		name := envName(entry)
		if !matchesAnyCompiled(name, removeM) {
			out = append(out, entry)
		}
	}
	return out
}

// StripPathSegment returns a copy of env with `dir` removed from any
// PATH= entry. Useful when forwarding env to a remote process that has
// no use for a local-only directory (e.g. the path-stub mount). If the
// segment isn't present, env is returned unchanged. If removing it
// empties PATH, the PATH entry is dropped entirely.
func StripPathSegment(env []string, dir string) []string {
	if dir == "" {
		return env
	}
	out := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, "PATH=") {
			out = append(out, entry)
			continue
		}
		val := entry[len("PATH="):]
		segs := strings.Split(val, ":")
		kept := segs[:0]
		for _, s := range segs {
			if s != dir {
				kept = append(kept, s)
			}
		}
		if len(kept) == 0 {
			continue // drop the PATH entry entirely
		}
		out = append(out, "PATH="+strings.Join(kept, ":"))
	}
	return out
}

func filterCompiled(env []string, keep []matcher, remove []matcher) []string {
	changed := parseChangedEnvs(env)

	out := make([]string, 0, len(env))
	for _, entry := range env {
		name := envName(entry)

		// Always strip the internal marker.
		if name == changedEnvsKey {
			continue
		}

		if matchesAnyCompiled(name, remove) {
			continue
		}

		if changed[name] || matchesAnyCompiled(name, keep) {
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
