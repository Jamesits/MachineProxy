//go:build backend_ssh

package ssh

import (
	"testing"
)

func TestParseServerVersion(t *testing.T) {
	cases := []struct {
		name    string
		banner  string
		wantOS  string
		wantErr bool
	}{
		{
			name:   "openssh ubuntu",
			banner: "SSH-2.0-OpenSSH_8.9p1 Ubuntu-3ubuntu0.1",
			wantOS: "linux",
		},
		{
			name:   "openssh debian",
			banner: "SSH-2.0-OpenSSH_8.0 Debian-4",
			wantOS: "linux",
		},
		{
			name:   "openssh freebsd",
			banner: "SSH-2.0-OpenSSH_8.6 FreeBSD-20210907",
			wantOS: "freebsd",
		},
		{
			name:   "openssh netbsd",
			banner: "SSH-2.0-OpenSSH_9.4 NetBSD_20240520",
			wantOS: "netbsd",
		},
		{
			name:   "openssh openbsd",
			banner: "SSH-2.0-OpenSSH_9.6 OpenBSD-7.4",
			wantOS: "openbsd",
		},
		{
			name:   "openssh for windows",
			banner: "SSH-2.0-OpenSSH_for_Windows_8.1",
			wantOS: "windows",
		},
		{
			name:   "dropbear embedded linux",
			banner: "SSH-2.0-dropbear_2022.83",
			wantOS: "linux",
		},
		{
			name:   "alpine",
			banner: "SSH-2.0-OpenSSH_9.3 Alpine-3.18",
			wantOS: "linux",
		},
		{
			name:    "no hint",
			banner:  "SSH-2.0-OpenSSH_9.0",
			wantErr: true,
		},
		{
			name:    "empty",
			banner:  "",
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, err := parseServerVersion(tc.banner)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got info=%+v", info)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if info.OS != tc.wantOS {
				t.Fatalf("OS = %q, want %q", info.OS, tc.wantOS)
			}
			if info.Arch != "" {
				t.Fatalf("expected empty arch, got %q", info.Arch)
			}
		})
	}
}
