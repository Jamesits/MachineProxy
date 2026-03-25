package execpolicy

import (
	"fmt"
	"path/filepath"
	"strings"
)

type Whitelist struct {
	entries map[string]struct{}
}

func NewWhitelist(paths []string) (*Whitelist, error) {
	entries := make(map[string]struct{}, len(paths))
	for _, p := range paths {
		clean := filepath.Clean(p)
		if !filepath.IsAbs(clean) {
			return nil, fmt.Errorf("whitelist entry must be absolute: %q", p)
		}
		entries[clean] = struct{}{}
	}
	return &Whitelist{entries: entries}, nil
}

func FromEnv(list string) (*Whitelist, error) {
	if strings.TrimSpace(list) == "" {
		return NewWhitelist(nil)
	}
	return NewWhitelist(strings.Split(list, ":"))
}

func (w *Whitelist) Allows(path string) bool {
	if w == nil {
		return false
	}
	_, ok := w.entries[filepath.Clean(path)]
	return ok
}
