package supervisor

import (
	"context"
	"reflect"
	"testing"
)

func TestSupervisorStartsNamespaceMountBrokerAndChildInOrder(t *testing.T) {
	f := &fakeDeps{}
	s := New(Deps{
		Backend:  f,
		NS:       f,
		FS:       f,
		Broker:   f,
		Launcher: f,
	})

	if err := s.Run(context.Background(), []string{"/usr/bin/make", "test"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"backend", "namespace", "fuse", "broker", "child"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %#v, want %#v", f.calls, want)
	}
}

func TestSupervisorRunsPathStubsBetweenFuseAndBroker(t *testing.T) {
	f := &fakeDeps{}
	s := New(Deps{
		Backend:   f,
		NS:        f,
		FS:        f,
		PathStubs: f,
		Broker:    f,
		Launcher:  f,
	})

	if err := s.Run(context.Background(), []string{"/usr/bin/make", "test"}); err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	want := []string{"backend", "namespace", "fuse", "pathstubs", "broker", "child"}
	if !reflect.DeepEqual(f.calls, want) {
		t.Fatalf("call order = %#v, want %#v", f.calls, want)
	}
}

type fakeDeps struct {
	calls []string
}

func (f *fakeDeps) StartBackend(context.Context) error {
	f.calls = append(f.calls, "backend")
	return nil
}

func (f *fakeDeps) EnterNamespace(context.Context) error {
	f.calls = append(f.calls, "namespace")
	return nil
}

func (f *fakeDeps) MountWorkspace(context.Context) error {
	f.calls = append(f.calls, "fuse")
	return nil
}

func (f *fakeDeps) BuildPathStubs(context.Context) error {
	f.calls = append(f.calls, "pathstubs")
	return nil
}

func (f *fakeDeps) StartBroker(context.Context) error {
	f.calls = append(f.calls, "broker")
	return nil
}

func (f *fakeDeps) RunChild(context.Context, []string) error {
	f.calls = append(f.calls, "child")
	return nil
}
