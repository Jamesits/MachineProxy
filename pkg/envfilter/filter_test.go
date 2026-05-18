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

func TestFilter_RegexKeep(t *testing.T) {
	env := []string{
		"AWS_ACCESS_KEY_ID=key",
		"AWS_SECRET_ACCESS_KEY=secret",
		"HOME=/home/test",
		"UNRELATED=val",
	}
	keep := []string{"/^AWS_/", "HOME"}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{
		"AWS_ACCESS_KEY_ID=key",
		"AWS_SECRET_ACCESS_KEY=secret",
		"HOME=/home/test",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_RegexRemove(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"DB_SECRET_TOKEN=abc",
		"API_SECRET_KEY=xyz",
		"PATH=/usr/bin",
	}
	keep := []string{"*"}
	remove := []string{"/SECRET/"}

	got := Filter(env, keep, remove)
	want := []string{
		"HOME=/home/test",
		"PATH=/usr/bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_MixedGlobAndRegex(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"LC_ALL=en",
		"CUSTOM_VAR_123=val",
		"CUSTOM_VAR_ABC=val",
		"UNRELATED=x",
	}
	// Keep HOME, LC_* (glob), and anything matching CUSTOM_VAR_\d+ (regex)
	keep := []string{"HOME", "LC_*", `/^CUSTOM_VAR_\d+$/`}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{
		"HOME=/home/test",
		"LC_ALL=en",
		"CUSTOM_VAR_123=val",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRemove(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"MPROXY_BROKER_SOCK=/tmp/mp.sock",
		"MPROXY_SHIM_PATH=/opt/shim",
		"PATH=/usr/bin",
	}
	remove := []string{"MPROXY_*"}

	got := Remove(env, remove)
	want := []string{
		"HOME=/home/test",
		"PATH=/usr/bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestRemove_Regex(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"UNSAFE_VAR=bad",
		"PATH=/usr/bin",
	}
	remove := []string{"/^UNSAFE_/"}

	got := Remove(env, remove)
	want := []string{
		"HOME=/home/test",
		"PATH=/usr/bin",
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestFilter_InvalidRegexSkipped(t *testing.T) {
	env := []string{
		"HOME=/home/test",
		"FOO=bar",
	}
	// Invalid regex pattern should be silently skipped.
	keep := []string{"HOME", "/[invalid/"}
	remove := []string{}

	got := Filter(env, keep, remove)
	want := []string{"HOME=/home/test"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_PrependedSegmentRemoved(t *testing.T) {
	env := []string{"HOME=/home/test", "PATH=/stub:/usr/bin:/bin"}
	got := StripPathSegment(env, "/stub")
	want := []string{"HOME=/home/test", "PATH=/usr/bin:/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_AppendedSegmentRemoved(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin:/stub"}
	got := StripPathSegment(env, "/stub")
	want := []string{"PATH=/usr/bin:/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_MidSegmentRemoved(t *testing.T) {
	env := []string{"PATH=/usr/bin:/stub:/bin"}
	got := StripPathSegment(env, "/stub")
	want := []string{"PATH=/usr/bin:/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_NoMatchUnchanged(t *testing.T) {
	env := []string{"PATH=/usr/bin:/bin"}
	got := StripPathSegment(env, "/stub")
	want := []string{"PATH=/usr/bin:/bin"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_DropsEmptyPath(t *testing.T) {
	env := []string{"HOME=/home/test", "PATH=/stub"}
	got := StripPathSegment(env, "/stub")
	want := []string{"HOME=/home/test"}
	if !slices.Equal(got, want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestStripPathSegment_EmptyDirNoop(t *testing.T) {
	env := []string{"PATH=/usr/bin"}
	got := StripPathSegment(env, "")
	if !slices.Equal(got, env) {
		t.Errorf("got %v, want %v", got, env)
	}
}
