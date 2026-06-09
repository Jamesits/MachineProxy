package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
)

// LocalCommandRule is a compiled local_commands entry that can match
// against executable pathnames. Supported forms:
//   - "/absolute/path"  — exact full path match
//   - "/regex/"         — regex matched against the full pathname
//   - "basename"        — matched against the last segment of the path
type LocalCommandRule struct {
	raw   string
	regex *regexp.Regexp // non-nil for /regex/ entries
	abs   string         // non-empty for /absolute/path entries
	base  string         // non-empty for basename entries
}

// CompileLocalCommand parses and validates a local_commands entry,
// returning a rule that can match pathnames.
func CompileLocalCommand(s string) (*LocalCommandRule, error) {
	if s == "" {
		return nil, errors.New("entry must not be empty")
	}

	startsSlash := strings.HasPrefix(s, "/")
	endsSlash := strings.HasSuffix(s, "/") && len(s) > 1

	switch {
	// /regex/ — delimited regex pattern
	case startsSlash && endsSlash:
		pattern := s[1 : len(s)-1]
		if pattern == "" {
			return nil, fmt.Errorf("regex pattern must not be empty: %q", s)
		}
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid regex in %q: %w", s, err)
		}
		return &LocalCommandRule{raw: s, regex: re}, nil

	// /absolute/path — full path match
	case startsSlash && !endsSlash:
		if !filepath.IsAbs(s) {
			return nil, fmt.Errorf("entry must be an absolute path: %q", s)
		}
		return &LocalCommandRule{raw: s, abs: s}, nil

	// basename — match against last segment of the executable path
	case !startsSlash && !strings.Contains(s, "/"):
		return &LocalCommandRule{raw: s, base: s}, nil

	// Reject: slashes only in the middle (e.g. "usr/bin/env")
	default:
		return nil, fmt.Errorf("entry %q has slashes in the middle; use an absolute path (/usr/bin/env), a regex (/pattern/), or a bare name (env)", s)
	}
}

// Match reports whether pathname matches this rule.
func (r *LocalCommandRule) Match(pathname string) bool {
	switch {
	case r.regex != nil:
		return r.regex.MatchString(pathname)
	case r.abs != "":
		return pathname == r.abs
	default:
		return filepath.Base(pathname) == r.base
	}
}

// MatchLocalCommand is a convenience that compiles entry and matches in one
// step. Returns false for malformed entries.
func MatchLocalCommand(entry, pathname string) bool {
	r, err := CompileLocalCommand(entry)
	if err != nil {
		return false
	}
	return r.Match(pathname)
}
