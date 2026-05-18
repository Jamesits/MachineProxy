//go:build backend_docker

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"path"
	"strings"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/client"
)

// copyFileToContainer writes a single file into the container at dst
// with the given mode. The destination's parent directory is created
// by CopyToContainer's behavior: if the parent doesn't exist, the
// upload fails — so we run `mkdir -p` via an exec first.
func copyFileToContainer(ctx context.Context, cli *client.Client, containerID, localPath, remotePath string, mode os.FileMode) error {
	// Read the file into a tar archive.
	src, err := os.Open(localPath)
	if err != nil {
		return fmt.Errorf("open local: %w", err)
	}
	defer src.Close()
	st, err := src.Stat()
	if err != nil {
		return fmt.Errorf("stat local: %w", err)
	}

	parent := path.Dir(remotePath)
	if parent != "" && parent != "/" && parent != "." {
		if err := mkdirInContainer(ctx, cli, containerID, parent); err != nil {
			return fmt.Errorf("mkdir %q in container: %w", parent, err)
		}
	}

	buf := &bytes.Buffer{}
	tw := tar.NewWriter(buf)
	hdr := &tar.Header{
		Name: path.Base(remotePath),
		Mode: int64(mode),
		Size: st.Size(),
	}
	if err := tw.WriteHeader(hdr); err != nil {
		return fmt.Errorf("tar header: %w", err)
	}
	if _, err := io.Copy(tw, src); err != nil {
		return fmt.Errorf("tar body: %w", err)
	}
	if err := tw.Close(); err != nil {
		return fmt.Errorf("tar close: %w", err)
	}

	return cli.CopyToContainer(ctx, containerID, parent, buf, container.CopyToContainerOptions{})
}

// mkdirInContainer creates dir (recursively) inside the container by
// running `mkdir -p` via a one-shot exec. We use a one-shot exec
// rather than the docker archive API because the latter has no
// "create parents" mode.
func mkdirInContainer(ctx context.Context, cli *client.Client, containerID, dir string) error {
	created, err := cli.ContainerExecCreate(ctx, containerID, container.ExecOptions{
		AttachStdout: true,
		AttachStderr: true,
		Cmd:          []string{"mkdir", "-p", dir},
	})
	if err != nil {
		return err
	}
	resp, err := cli.ContainerExecAttach(ctx, created.ID, container.ExecAttachOptions{})
	if err != nil {
		return err
	}
	defer resp.Close()
	// Drain output; mkdir -p is quick. The output isn't useful unless
	// it fails — keep it for the error path.
	var combined strings.Builder
	_, _ = io.Copy(&combined, resp.Reader)
	insp, err := cli.ContainerExecInspect(ctx, created.ID)
	if err != nil {
		return err
	}
	if insp.ExitCode != 0 {
		return fmt.Errorf("mkdir -p %q failed (exit %d): %s", dir, insp.ExitCode, combined.String())
	}
	return nil
}
