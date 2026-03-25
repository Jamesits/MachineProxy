package envfilter

import (
	"slices"
	"testing"
)

func TestFilter_ChangedVarsAlwaysForwarded(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"MY_TOKEN=secret",
		"MPROXY_CHANGED_ENVS=MY_TOKEN",
	}
	keep := []string{"HOME"}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{"HOME=/home/test", "MY_TOKEN=secret"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_RemoveOverridesChanged(t *testing.T) {
	env := []string{
		"LD_PRELOAD=/lib/hook.so",
		"MPROXY_CHANGED_ENVS=LD_PRELOAD",
	}
	keep := []string{}
	remove := []string{"LD_*"}

	got := Filter(env, keep, remove)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestFilter_KeepPatterns(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"PATH=/usr/bin",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
		"SECRET=nope",
	}
	keep := []string{"HOME", "PATH", "LC_*"}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{
		"HOME=/home/test",
		"PATH=/usr/bin",
		"LC_ALL=en_US.UTF-8",
		"LC_CTYPE=en_US.UTF-8",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_MproxyVarsStripped(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"MPROXY_BROKER_SOCK=/tmp/mp.sock",
		"MPROXY_SHIM_PATH=/opt/shim",
		"MPROXY_WHITELIST=/usr/bin/env",
		"MPROXY_HOOK_BYPASS=1",
		"MPROXY_CHANGED_ENVS=HOME",
	}
	keep := []string{"HOME"}
	remove := []string{"MPROXY_*"}

	got := Filter(env, keep, remove)
	want := []string{"HOME=/home/test"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_NoChangedEnvsVar(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"FOO=bar",
	}
	keep := []string{"HOME"}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{"HOME=/home/test"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_EmptyEnv(t *testing.T) {
	got := Filter(nil, []string{"*"}, nil)
	if len(got) != 0 {
		t.Errorf("expected empty, got %v", got)
	}
}

func TestFilter_ChangedEnvsStrippedFromOutput(t *testing.T) {
	env := []string{
		"FOO=bar",
		"MPROXY_CHANGED_ENVS=FOO",
	}
	keep := []string{}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{"FOO=bar"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}
