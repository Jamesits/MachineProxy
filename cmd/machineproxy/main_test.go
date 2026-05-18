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
			name: "bare destination with -- and command",
			args: []string{"user@host.example", "--", "echo", "hello"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "user@host.example",
				cmd:         []string{"echo", "hello"},
			},
		},
		{
			name: "destination without -- treats first positional as destination",
			args: []string{"host.example", "echo", "hello"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "host.example",
				cmd:         []string{"echo", "hello"},
			},
		},
		{
			name: "scheme-prefixed ssh destination",
			args: []string{"ssh://alice@example.com:2222", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "ssh://alice@example.com:2222",
				cmd:         []string{"id"},
			},
		},
		{
			name: "scheme-prefixed docker destination",
			args: []string{"docker://my-box", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "docker://my-box",
				cmd:         []string{"id"},
			},
		},
		{
			name: "--backend flag sets backend",
			args: []string{"--backend", "docker", "my-box", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				backend:     "docker",
				destination: "my-box",
				cmd:         []string{"id"},
			},
		},
		{
			name: "port flag",
			args: []string{"-p", "2222", "user@host.example", "--", "uname", "-a"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				port:        2222,
				destination: "user@host.example",
				cmd:         []string{"uname", "-a"},
			},
		},
		{
			name: "-l sets user",
			args: []string{"-l", "alice", "bob@host.example", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				user:        "alice",
				destination: "bob@host.example",
				cmd:         []string{"id"},
			},
		},
		{
			name: "--port and --login long aliases",
			args: []string{"--port", "2222", "--login", "alice", "host.example", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				port:        2222,
				user:        "alice",
				destination: "host.example",
				cmd:         []string{"id"},
			},
		},
		{
			name: "--arch overrides remote arch",
			args: []string{"--arch", "arm64", "host.example", "--", "uname", "-m"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "host.example",
				arch:        "arm64",
				cmd:         []string{"uname", "-m"},
			},
		},
		{
			name: "repeated -v accumulates",
			args: []string{"-v", "/a", "-v", "/b:/c", "host", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "host",
				mounts:      []string{"/a", "/b:/c"},
				cmd:         []string{"id"},
			},
		},
		{
			name: "-w sets workdir",
			args: []string{"-w", "/work", "host", "--", "id"},
			want: cliArgs{
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "host",
				workdir:     "/work",
				cmd:         []string{"id"},
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
				cfgPath:     "/etc/machineproxy/machineproxy.toml",
				destination: "user@host.example",
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
