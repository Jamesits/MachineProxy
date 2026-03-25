package execpolicy

import "testing"

func TestWhitelistAllowsExactPath(t *testing.T) {
	w, err := NewWhitelist([]string{"/usr/bin/env", "/bin/sh"})
	if err != nil {
		t.Fatalf("NewWhitelist() error = %v", err)
	}
	if !w.Allows("/usr/bin/env") {
		t.Fatalf("expected /usr/bin/env to be allowed")
	}
	if w.Allows("/usr/bin/python3") {
		t.Fatalf("expected /usr/bin/python3 to be denied")
	}
}

func TestWhitelistRejectsRelativePaths(t *testing.T) {
	if _, err := NewWhitelist([]string{"python"}); err == nil {
		t.Fatalf("expected relative path whitelist to fail")
	}
}
