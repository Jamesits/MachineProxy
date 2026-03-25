package remoteexec

import "testing"

func TestBuildRemoteCommandQuotesCwdAndArgs(t *testing.T) {
	req := Request{
		Path: "/usr/bin/python3",
		Argv: []string{"python3", "-c", "print('ok')"},
		Cwd:  "/workspace/hello world",
	}

	got := buildRemoteCommand(req)
	want := `cd '/workspace/hello world' && '/usr/bin/python3' '-c' 'print('\''ok'\'')'`
	if got != want {
		t.Fatalf("buildRemoteCommand() = %q, want %q", got, want)
	}
}
