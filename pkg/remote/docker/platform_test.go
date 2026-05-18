//go:build backend_docker

package docker

import "testing"

func TestPlatformFromInfo(t *testing.T) {
	cases := []struct {
		name        string
		osType      string
		arch        string
		wantOS      string
		wantArch    string
		wantVariant string
	}{
		{"linux amd64", "linux", "x86_64", "linux", "amd64", ""},
		{"linux arm64", "linux", "aarch64", "linux", "arm64", ""},
		{"linux armv7", "linux", "armv7l", "linux", "arm", "v7"},
		{"linux armv6", "linux", "armv6l", "linux", "arm", "v6"},
		{"linux 386", "linux", "i686", "linux", "386", ""},
		{"linux riscv64", "linux", "riscv64", "linux", "riscv64", ""},
		{"linux ppc64le", "linux", "ppc64le", "linux", "ppc64le", ""},
		{"linux s390x", "linux", "s390x", "linux", "s390x", ""},
		{"linux loong64", "linux", "loongarch64", "linux", "loong64", ""},
		{"darwin arm64", "darwin", "arm64", "darwin", "arm64", ""},
		{"windows amd64", "windows", "x86_64", "windows", "amd64", ""},
		{"freebsd amd64", "freebsd", "amd64", "freebsd", "amd64", ""},
		{"unknown arch", "linux", "vax", "linux", "", ""},
		{"unknown os", "plan9", "x86_64", "", "amd64", ""},
		{"all unknown", "weirdos", "weirdarch", "", "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := platformFromInfo(tc.osType, tc.arch)
			if got.OS != tc.wantOS {
				t.Errorf("OS = %q, want %q", got.OS, tc.wantOS)
			}
			if got.Arch != tc.wantArch {
				t.Errorf("Arch = %q, want %q", got.Arch, tc.wantArch)
			}
			if got.Variant != tc.wantVariant {
				t.Errorf("Variant = %q, want %q", got.Variant, tc.wantVariant)
			}
		})
	}
}
