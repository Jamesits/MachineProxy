//go:build darwin

package main

import (
	"slices"
	"testing"
)

func TestRemapPWDEnv(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want []string
	}{
		{
			name: "remaps PWD under from",
			env:  []string{"HOME=/h", "PWD=/ws/sub", "TERM=xterm"},
			want: []string{"HOME=/h", "PWD=/remote/sub", "TERM=xterm"},
		},
		{
			name: "exact prefix",
			env:  []string{"PWD=/ws"},
			want: []string{"PWD=/remote"},
		},
		{
			name: "PWD outside from unchanged",
			env:  []string{"PWD=/other/place"},
			want: []string{"PWD=/other/place"},
		},
		{
			name: "no PWD entry unchanged",
			env:  []string{"HOME=/h", "TERM=xterm"},
			want: []string{"HOME=/h", "TERM=xterm"},
		},
		{
			name: "sibling prefix not matched",
			env:  []string{"PWD=/wsX/y"},
			want: []string{"PWD=/wsX/y"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := remapPWDEnv(c.env, "/ws", "/remote")
			if !slices.Equal(got, c.want) {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}
