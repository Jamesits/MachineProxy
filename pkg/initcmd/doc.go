// Package initcmd resolves the initial command and its shebang helper
// chain (including env-style shebangs) before the container process
// starts. It receives CLI command names, PATH values, and skip
// directories from the runtime, and feeds local-command augmentation
// plus the launcher-ready executable path.
package initcmd
