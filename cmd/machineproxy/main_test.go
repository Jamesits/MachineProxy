package main

import (
	"reflect"
	"testing"
)

func TestParseCLIArgs(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want cliArgs
	}{
		{
			name: "config only with -- and command",
			args: []string{"--config", "/tmp/cfg.yaml", "--", "cat", "/etc/os-release"},
			want: cliArgs{
				cfgPath: "/tmp/cfg.yaml",
				cmd:     []string{"cat", "/etc/os-release"},
			},
		},
		{
			name: "destination user@host with -- and command",
			args: []string{"user@host.example", "--", "echo", "hello"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				user:    "user",
				host:    "host.example",
				cmd:     []string{"echo", "hello"},
			},
		},
		{
			name: "destination without -- treats first positional as host",
			args: []string{"host.example", "echo", "hello"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host.example",
				cmd:     []string{"echo", "hello"},
			},
		},
		{
			name: "port flag and full destination",
			args: []string{"-p", "2222", "user@host.example", "--", "uname", "-a"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				port:    2222,
				user:    "user",
				host:    "host.example",
				cmd:     []string{"uname", "-a"},
			},
		},
		{
			name: "-l overrides user from user@host",
			args: []string{"-l", "alice", "bob@host.example", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				user:    "alice",
				host:    "host.example",
				cmd:     []string{"id"},
			},
		},
		{
			name: "--port and --login long aliases",
			args: []string{"--port", "2222", "--login", "alice", "host.example", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				port:    2222,
				user:    "alice",
				host:    "host.example",
				cmd:     []string{"id"},
			},
		},
		{
			name: "--arch overrides remote arch",
			args: []string{"--arch", "arm64", "host.example", "--", "uname", "-m"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host.example",
				arch:    "arm64",
				cmd:     []string{"uname", "-m"},
			},
		},
		{
			name: "repeated -v accumulates in CLI order",
			args: []string{"-v", "/a", "-v", "/b:/c", "host", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host",
				mounts:  []string{"/a", "/b:/c"},
				cmd:     []string{"id"},
			},
		},
		{
			name: "--mount long alias accumulates with -v",
			args: []string{"-v", "/a", "--mount", "/b", "host", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host",
				mounts:  []string{"/a", "/b"},
				cmd:     []string{"id"},
			},
		},
		{
			name: "-w sets workdir",
			args: []string{"-w", "/work", "host", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host",
				workdir: "/work",
				cmd:     []string{"id"},
			},
		},
		{
			name: "--workdir long alias",
			args: []string{"--workdir", "/work", "host", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				host:    "host",
				workdir: "/work",
				cmd:     []string{"id"},
			},
		},
		{
			name: "ipv6 host inside user@host",
			args: []string{"user@2001:db8::1", "--", "id"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				user:    "user",
				host:    "2001:db8::1",
				cmd:     []string{"id"},
			},
		},
		{
			name: "version flag short-circuits",
			args: []string{"--version"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				showVersion: true,
			},
		},
		{
			name: "double-dash immediately after flags is preserved",
			args: []string{"--config", "/tmp/cfg.yaml", "-p", "22", "--", "echo", "hi"},
			want: cliArgs{
				cfgPath: "/tmp/cfg.yaml",
				port:    22,
				cmd:     []string{"echo", "hi"},
			},
		},
		{
			name: "bare destination with no command",
			args: []string{"user@host.example"},
			want: cliArgs{
				cfgPath: "/etc/machineproxy/machineproxy.toml",
				user:    "user",
				host:    "host.example",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseCLIArgs(tc.args)
			if err != nil {
				t.Fatalf("parseCLIArgs(%v) error = %v", tc.args, err)
			}
			if !reflect.DeepEqual(*got, tc.want) {
				t.Fatalf("parseCLIArgs(%v) = %+v, want %+v", tc.args, *got, tc.want)
			}
		})
	}
}

func TestParseCLIArgsRejectsExtraPositionalsBeforeSeparator(t *testing.T) {
	_, err := parseCLIArgs([]string{"user@host", "extra", "--", "cmd"})
	if err == nil {
		t.Fatal("expected error for extra positional args before --, got nil")
	}
}

func TestSplitUserHost(t *testing.T) {
	cases := []struct {
		in       string
		wantUser string
		wantHost string
	}{
		{"host.example", "", "host.example"},
		{"user@host.example", "user", "host.example"},
		{"user@2001:db8::1", "user", "2001:db8::1"},
		{"@host", "", "host"},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			u, h := splitUserHost(tc.in)
			if u != tc.wantUser || h != tc.wantHost {
				t.Fatalf("splitUserHost(%q) = (%q, %q), want (%q, %q)", tc.in, u, h, tc.wantUser, tc.wantHost)
			}
		})
	}
}
