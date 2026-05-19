// Package envfilter selects which environment variables are forwarded to
// remote commands. It receives environment slices and keep/remove patterns
// from broker and agent configuration, consumes MPROXY_CHANGED_ENVS from
// the exec interception layer when present, and feeds sanitized env slices
// to remoteexec and mproxy-agent.
package envfilter
