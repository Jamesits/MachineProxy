//go:build backend_ssh

package ssh

import (
	"io"

	"github.com/jamesits/machineproxy/pkg/sshconn"
)

// sshSessionAdapter promotes sshconn.Session to remote.Session. The
// interfaces are structurally identical today, but going through an
// adapter keeps the remote package free of any direct golang.org/x/crypto
// dependency.
type sshSessionAdapter struct{ sshconn.Session }

func (a sshSessionAdapter) StdinPipe() (io.WriteCloser, error) { return a.Session.StdinPipe() }
func (a sshSessionAdapter) StdoutPipe() (io.Reader, error)     { return a.Session.StdoutPipe() }
func (a sshSessionAdapter) StderrPipe() (io.Reader, error)     { return a.Session.StderrPipe() }
func (a sshSessionAdapter) Setenv(name, value string) error    { return a.Session.Setenv(name, value) }
func (a sshSessionAdapter) Start(cmd string) error             { return a.Session.Start(cmd) }
func (a sshSessionAdapter) Wait() error                        { return a.Session.Wait() }
func (a sshSessionAdapter) Close() error                       { return a.Session.Close() }
