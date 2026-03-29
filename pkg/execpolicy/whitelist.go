package execpolicy

import (
	"fmt"
	"path/filepath"
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

func (w *Whitelist) Allows(path string) bool {
	if w == nil {
		return false
	}
	_, ok := w.entries[filepath.Clean(path)]
	return ok
}
