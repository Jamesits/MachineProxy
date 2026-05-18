// Package subreaper provides a portable wrapper around the
// "subreaper" / orphan-cleanup primitive on each supported OS:
//
//   - Linux:   prctl(PR_SET_CHILD_SUBREAPER) + SIGCHLD reaping
//   - FreeBSD: procctl(PROC_REAP_ACQUIRE) + SIGCHLD reaping
//   - macOS:   no-op (orphans reparent to launchd)
//   - Windows: kill-on-close Job Object; orphans die when this
//     process exits and the OS releases the job handle
//
// A typical lifecycle is:
//
//	if err := subreaper.Setup(); err != nil { ... }
//	go subreaper.Reap(ctx)
package subreaper
