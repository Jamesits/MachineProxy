package remote

import "testing"

func TestParseDestination(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		def         Type
		wantType    Type
		wantUser    string
		wantHost    string
		wantPort    int
		expectError bool
	}{
		{"bare host defaults ssh", "example.com", TypeSSH, TypeSSH, "", "example.com", 0, false},
		{"bare user@host", "alice@example.com", TypeSSH, TypeSSH, "alice", "example.com", 0, false},
		{"bare host:port", "example.com:2222", TypeSSH, TypeSSH, "", "example.com", 2222, false},
		{"bare user@host:port", "alice@example.com:2222", TypeSSH, TypeSSH, "alice", "example.com", 2222, false},
		{"ssh scheme bare", "ssh://example.com", TypeSSH, TypeSSH, "", "example.com", 0, false},
		{"ssh scheme full", "ssh://alice@example.com:2222", TypeSSH, TypeSSH, "alice", "example.com", 2222, false},
		{"ssh bracketed ipv6", "ssh://alice@[::1]:2222", TypeSSH, TypeSSH, "alice", "::1", 2222, false},
		{"ssh bare ipv6 no port", "ssh://2001:db8::1", TypeSSH, TypeSSH, "", "2001:db8::1", 0, false},
		{"docker scheme", "docker://mp-test", TypeSSH, TypeDocker, "", "mp-test", 0, false},
		{"docker id-ish", "docker://abc123", TypeSSH, TypeDocker, "", "abc123", 0, false},
		{"default docker bare", "mp-test", TypeDocker, TypeDocker, "", "mp-test", 0, false},
		{"empty", "", TypeSSH, "", "", "", 0, true},
		{"unknown scheme", "k8s://pod", TypeSSH, "", "", "", 0, true},
		{"docker empty", "docker://", TypeSSH, "", "", "", 0, true},
		{"docker with slash", "docker://has/slash", TypeSSH, "", "", "", 0, true},
		{"ssh missing host", "ssh://alice@", TypeSSH, "", "", "", 0, true},
		{"ssh bad port", "ssh://example.com:notaport", TypeSSH, "", "", "", 0, true},
		{"ssh out of range port", "ssh://example.com:0", TypeSSH, "", "", "", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ParseDestination(tc.in, tc.def)
			if tc.expectError {
				if err == nil {
					t.Fatalf("expected error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Type != tc.wantType {
				t.Errorf("Type = %q, want %q", got.Type, tc.wantType)
			}
			if got.User != tc.wantUser {
				t.Errorf("User = %q, want %q", got.User, tc.wantUser)
			}
			if got.Host != tc.wantHost {
				t.Errorf("Host = %q, want %q", got.Host, tc.wantHost)
			}
			if got.Port != tc.wantPort {
				t.Errorf("Port = %d, want %d", got.Port, tc.wantPort)
			}
			if got.Raw != tc.in {
				t.Errorf("Raw = %q, want %q", got.Raw, tc.in)
			}
		})
	}
}
