package adapters

import (
    "context"
    "testing"
)

func TestCommandExecutor_RejectsUnsafeCommand(t *testing.T) {
    e := &RealCommandExecutor{}
    _, err := e.Execute(context.Background(), "ip;rm", "addr")
    if err == nil {
        t.Fatalf("expected validation error for unsafe command")
    }
}

func TestCommandExecutor_RejectsUnsafeArg(t *testing.T) {
    e := &RealCommandExecutor{}
    _, err := e.Execute(context.Background(), "ip", "addr", "`whoami`")
    if err == nil {
        t.Fatalf("expected validation error for unsafe arg")
    }
}

