package main

import (
	"sort"
	"strings"

	"github.com/jamesits/machineproxy/pkg/config"
	"github.com/jamesits/machineproxy/pkg/pathstub"
)

// buildPathMapper returns a function that translates a container-local path
// to its remote-side equivalent. The broker uses it to rewrite the Cwd and
// Path of forwarded exec requests so the agent resolves them on the remote
// host. It composes two sources:
//
//   - PATH-stub lookups: a path directly under stubPrefix is resolved to the
//     stub's recorded remote path (exact stub-name match), checked first.
//   - mount prefixes: each mount whose container and remote paths differ
//     rewrites its container-path prefix to its remote-path prefix.
//
// Mounts are matched longest-container-path first, so a nested mount wins
// over a parent. Identity mounts (container == remote, e.g. the aliases added
// by container.auto_identity_mounts) need no rewrite and are skipped. Returns
// nil when there is nothing to translate, which the broker treats as
// pass-through.
func buildPathMapper(mounts []config.Mount, stubPrefix string, stubMap map[string]pathstub.Entry) func(string) string {
	hasStubRewrite := len(stubMap) > 0

	type prefixPair struct{ from, to string }
	var pairs []prefixPair
	for _, m := range mounts {
		if m.ContainerPath == m.RemotePath {
			continue
		}
		pairs = append(pairs, prefixPair{from: m.ContainerPath, to: m.RemotePath})
	}
	sort.SliceStable(pairs, func(i, j int) bool {
		return len(pairs[i].from) > len(pairs[j].from)
	})

	if !hasStubRewrite && len(pairs) == 0 {
		return nil
	}

	return func(p string) string {
		if hasStubRewrite && strings.HasPrefix(p, stubPrefix+"/") {
			name := p[len(stubPrefix)+1:]
			if e, ok := stubMap[name]; ok {
				return e.RemotePath
			}
		}
		for _, pp := range pairs {
			if mapped, ok := config.RewritePathPrefix(p, pp.from, pp.to); ok {
				return mapped
			}
		}
		return p
	}
}
