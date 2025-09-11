package network

import (
    "context"
    "os"
    "strings"
    "testing"
    "time"

    "github.com/sirupsen/logrus"
    "multinic-agent/internal/domain/entities"
)

// stub executor capturing calls
type stubExec struct{
    calls [][]string
}

func (s *stubExec) Execute(ctx context.Context, cmd string, args ...string) ([]byte, error) {
    s.calls = append(s.calls, append([]string{cmd}, args...))
    return []byte(""), nil
}

func (s *stubExec) ExecuteWithTimeout(ctx context.Context, _ time.Duration, cmd string, args ...string) ([]byte, error) {
    s.calls = append(s.calls, append([]string{cmd}, args...))
    // Provide canned output for ip -o link show
    if cmd == "ip" && len(args) >= 3 && args[0] == "-o" && args[1] == "link" && args[2] == "show" {
        // include a line with ens7 and a MAC to ensure findInterfaceByMAC succeeds
        out := "2: ens7: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\\tlink/ether fa:16:3e:11:4c:d1 brd ff:ff:ff:ff:ff:ff"
        return []byte(out), nil
    }
    return []byte(""), nil
}

// minimal file system stub (in-memory)
type memFS struct{ files map[string][]byte }

func (m *memFS) ReadFile(path string) ([]byte, error) {
    if b, ok := m.files[path]; ok { return b, nil }
    return nil, os.ErrNotExist
}
func (m *memFS) WriteFile(path string, data []byte, perm os.FileMode) error {
    if m.files == nil { m.files = map[string][]byte{} }
    _ = perm // ignore in stub
    m.files[path] = append([]byte(nil), data...)
    return nil
}
func (m *memFS) Exists(path string) bool { _, ok := m.files[path]; return ok }
func (m *memFS) MkdirAll(path string, perm os.FileMode) error { return nil }
func (m *memFS) Remove(path string) error { delete(m.files, path); return nil }
func (m *memFS) ListFiles(path string) ([]string, error) { return []string{}, nil }


func TestNetplanConfigure_PersistOnly_WithSetName(t *testing.T) {
    exec := &stubExec{}
    fs := &memFS{files: map[string][]byte{}}
    logger := newTestLogger()
    adapter := NewNetplanAdapter(exec, fs, logger)

    ni, err := entities.NewNetworkInterface(1, "fa:16:3e:11:4c:d1", "node", "11.11.11.107", "11.11.11.0/24", 1450)
    if err != nil { t.Fatalf("new iface: %v", err) }
    name, _ := entities.NewInterfaceName("multinic0")

    if err := adapter.Configure(context.Background(), *ni, *name); err != nil {
        t.Fatalf("configure: %v", err)
    }

    // verify file created with set-name
    cfg := "/etc/netplan/90-multinic0.yaml"
    b, err := fs.ReadFile(cfg)
    if err != nil { t.Fatalf("read cfg: %v", err) }
    s := string(b)
    if !strings.Contains(s, "set-name: multinic0") {
        t.Fatalf("expected set-name in netplan yaml, got:\n%s", s)
    }
    if !strings.Contains(s, "macaddress: fa:16:3e:11:4c:d1") {
        t.Fatalf("expected macaddress in yaml")
    }
    // ensure no netplan try/apply executed
    for _, c := range exec.calls {
        if len(c) > 0 && (c[0] == "netplan" || (c[0] == "nsenter" && len(c) > 6 && c[len(c)-2] == "netplan")) {
            t.Fatalf("unexpected netplan invocation: %#v", c)
        }
    }
}

func TestNetplanRollback_RemovesFileOnly(t *testing.T) {
    exec := &stubExec{}
    fs := &memFS{files: map[string][]byte{"/etc/netplan/90-multinic0.yaml": []byte("test")}}
    logger := newTestLogger()
    adapter := NewNetplanAdapter(exec, fs, logger)

    if err := adapter.Rollback(context.Background(), "multinic0"); err != nil {
        t.Fatalf("rollback: %v", err)
    }
    if fs.Exists("/etc/netplan/90-multinic0.yaml") {
        t.Fatalf("expected file removed on rollback")
    }
    // ensure no netplan apply executed
    for _, c := range exec.calls {
        if len(c) > 0 && c[0] == "netplan" {
            t.Fatalf("unexpected netplan invocation on rollback: %#v", c)
        }
    }
}

// minimal JSON logger without output
func newTestLogger() *logrus.Logger {
    l := logrus.New()
    l.SetLevel(logrus.PanicLevel)
    return l
}
