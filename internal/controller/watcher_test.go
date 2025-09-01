package controller

import (
    "context"
    "testing"
)

// Minimal test to ensure handler invokes Reconcile with name/namespace
func TestWatcher_HandleCR_InvokesReconcile(t *testing.T) {
    calls := 0
    ctrl := &Controller{}
    w := NewWatcher(ctrl, "multinic-system")
    w.Reconcile = func(ctx context.Context, ns, name string) error {
        calls++
        if ns != "multinic-system" || name != "worker-node-01" {
            t.Fatalf("unexpected args: %s/%s", ns, name)
        }
        return nil
    }
    obj := &fakeMeta{name: "worker-node-01", namespace: "multinic-system"}
    w.handleCR(obj)
    if calls != 1 { t.Fatalf("expected 1 call, got %d", calls) }
}

type fakeMeta struct{ name, namespace string }
func (f *fakeMeta) GetName() string      { return f.name }
func (f *fakeMeta) GetNamespace() string { return f.namespace }
