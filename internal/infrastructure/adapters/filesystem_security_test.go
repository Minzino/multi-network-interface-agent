package adapters

import (
    "path/filepath"
    "testing"

    domconst "multinic-agent/internal/domain/constants"
)

func TestIsSafeConfigPath(t *testing.T) {
    cases := []struct{
        path string
        ok   bool
    }{
        {filepath.Join(domconst.DefaultBackupDir, "iface.yaml"), true},
        {"/etc/passwd", false},
        {"/tmp/hack", false},
        {"../etc/shadow", false},
        {"", false},
    }
    for _, c := range cases {
        if got := isSafeConfigPath(c.path); got != c.ok {
            t.Fatalf("isSafeConfigPath(%q)=%v want %v", c.path, got, c.ok)
        }
    }
}

