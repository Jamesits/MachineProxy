// Package pathstub builds and serves a read-only FUSE directory that
// exposes one stub per executable reachable through the remote machine's
// $PATH. Reads on a stub proxy to the corresponding remote binary via
// SFTP; exec()-ing a stub gets rewritten by the broker's PathMapper to
// invoke the real remote path through mproxy-agent.
package pathstub

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/jamesits/machineproxy/pkg/agentproto"
	"github.com/jamesits/machineproxy/pkg/config"
)

// EnumerateLocally walks the listed PATH-style directories and returns
// one entry per executable file. If paths is empty, the agent's own
// $PATH is used (split on ":"). Within each directory entries are
// listed in name order; across directories the first occurrence of a
// name wins (mirroring real PATH lookup precedence).
//
// Symlinks are followed (os.Stat) so the returned size/mtime/mode reflect
// the binary the kernel would execute. Non-regular files, directories,
// and files lacking any execute bit are skipped.
func EnumerateLocally(paths []string) ([]agentproto.PathInfoEntry, error) {
	if len(paths) == 0 {
		paths = splitPath(os.Getenv("PATH"))
	}

	seen := make(map[string]struct{})
	var out []agentproto.PathInfoEntry

	for _, dir := range paths {
		if dir == "" {
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			// A non-existent or unreadable PATH dir is normal; skip it.
			continue
		}
		for _, de := range entries {
			name := de.Name()
			if _, dup := seen[name]; dup {
				continue
			}
			fullPath := filepath.Join(dir, name)
			st, err := os.Stat(fullPath)
			if err != nil {
				continue
			}
			if !st.Mode().IsRegular() {
				continue
			}
			if st.Mode().Perm()&0o111 == 0 {
				continue
			}
			seen[name] = struct{}{}
			out = append(out, agentproto.PathInfoEntry{
				Name:       name,
				RemotePath: fullPath,
				Mode:       uint32(st.Mode().Perm()),
				Size:       st.Size(),
				MTimeNanos: st.ModTime().UnixNano(),
			})
		}
	}

	return out, nil
}

// splitPath splits a colon-delimited PATH-style string. Unlike
// strings.Split, an empty input yields an empty slice (not [""]).
func splitPath(p string) []string {
	if p == "" {
		return nil
	}
	return strings.Split(p, ":")
}

// FilterLocalCommands removes entries whose synthesised stub path
// (mountPath/<name>) would be matched by any rule in localCommands.
// This prevents the tracer from shadowing a binary the user explicitly
// opted to run locally with a remote-routing stub. mountPath is the
// configured container-side directory where the stub FUSE is bind-mounted.
//
// Rule-by-rule behaviour (mirrors config.LocalCommandRule.Match):
//   - basename rule (e.g. "env")          → filters out the matching stub.
//   - regex   rule (e.g. "/^python.*/")    → matched against the full
//     synthetic path; rules anchored to "^/" or that include directory
//     components will not match.
//   - absolute rule (e.g. "/usr/bin/env") → never filters, because the
//     stub's pathname is <mountPath>/<name>, not /usr/bin/<name>. Use a
//     basename rule if you want stub-level shadowing.
//
// Invalid rules are silently skipped (the loader has already validated
// them; this is a defence-in-depth pass).
func FilterLocalCommands(entries []agentproto.PathInfoEntry, localCommands []string, mountPath string) []agentproto.PathInfoEntry {
	if len(localCommands) == 0 {
		return entries
	}
	rules := make([]*config.LocalCommandRule, 0, len(localCommands))
	for _, raw := range localCommands {
		r, err := config.CompileLocalCommand(raw)
		if err != nil {
			continue
		}
		rules = append(rules, r)
	}
	if len(rules) == 0 {
		return entries
	}

	out := entries[:0]
	for _, e := range entries {
		pseudo := mountPath + "/" + e.Name
		shadowed := false
		for _, r := range rules {
			if r.Match(pseudo) {
				shadowed = true
				break
			}
		}
		if !shadowed {
			out = append(out, e)
		}
	}
	return out
}
