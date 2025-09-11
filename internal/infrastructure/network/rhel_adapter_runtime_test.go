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

// stubs reused
type rhelStubExec struct{ calls [][]string }
func (s *rhelStubExec) Execute(ctx context.Context, cmd string, args ...string) ([]byte, error) { s.calls = append(s.calls, append([]string{cmd}, args...)); return []byte(""), nil }
func (s *rhelStubExec) ExecuteWithTimeout(ctx context.Context, _ time.Duration, cmd string, args ...string) ([]byte, error) {
    s.calls = append(s.calls, append([]string{cmd}, args...))
    // ip -o link show for MAC discovery
    if cmd == "ip" && len(args) >= 3 && args[0] == "-o" && args[1] == "link" && args[2] == "show" {
        out := "2: ens7: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\tlink/ether fa:16:3e:11:4c:d1 brd ff:ff:ff:ff:ff:ff"
        return []byte(out), nil
    }
    if cmd == "ip" && len(args) >= 1 && args[0] == "link" { // non -o path used in findDeviceByMAC
        out := "2: ens7: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\n    link/ether fa:16:3e:11:4c:d1 brd ff:ff:ff:ff:ff:ff"
        return []byte(out), nil
    }
    if cmd == "nsenter" {
        // find embedded ip command
        for i := 0; i < len(args); i++ {
            if args[i] == "ip" {
                // emulate `ip link show`
                if i+2 < len(args) && args[i+1] == "link" && args[i+2] == "show" {
                    out := "2: ens7: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1450 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\n    link/ether fa:16:3e:11:4c:d1 brd ff:ff:ff:ff:ff:ff"
                    return []byte(out), nil
                }
            }
        }
    }
    if cmd == "ip" && len(args) >= 2 && args[0] == "link" && args[1] == "show" { return []byte(""), nil }
    return []byte(""), nil
}

type rhelMemFS struct{ files map[string][]byte }
func (m *rhelMemFS) ReadFile(p string) ([]byte, error) { b, ok := m.files[p]; if !ok { return nil, os.ErrNotExist }; return b, nil }
func (m *rhelMemFS) WriteFile(p string, d []byte, _ os.FileMode) error { if m.files == nil { m.files = map[string][]byte{} }; m.files[p] = append([]byte(nil), d...); return nil }
func (m *rhelMemFS) Exists(p string) bool { _, ok := m.files[p]; return ok }
func (m *rhelMemFS) MkdirAll(p string, _ os.FileMode) error { return nil }
func (m *rhelMemFS) Remove(p string) error { delete(m.files, p); return nil }
func (m *rhelMemFS) ListFiles(p string) ([]string, error) { return []string{}, nil }

func TestRHELConfigure_PersistOnly_90Prefix(t *testing.T) {
    exec := &rhelStubExec{}
    fs := &rhelMemFS{files: map[string][]byte{}}
    lg := logrus.New(); lg.SetLevel(logrus.PanicLevel)
    ad := NewRHELAdapter(exec, fs, lg)

    ni, err := entities.NewNetworkInterface(0, "fa:16:3e:11:4c:d1", "node", "11.11.11.107", "11.11.11.0/24", 1450)
    if err != nil { t.Fatalf("ni: %v", err) }
    nm, _ := entities.NewInterfaceName("multinic0")

    if err := ad.Configure(context.Background(), *ni, *nm); err != nil { t.Fatalf("configure: %v", err) }

    // expect /etc/systemd/network/90-multinic0.link and /etc/NetworkManager/system-connections/90-multinic0.nmconnection
    link := "/etc/systemd/network/90-multinic0.link"
    nmconn := "/etc/NetworkManager/system-connections/90-multinic0.nmconnection"
    if !fs.Exists(link) || !fs.Exists(nmconn) { t.Fatalf("persist files not created: %v %v", fs.Exists(link), fs.Exists(nmconn)) }
    b, _ := fs.ReadFile(nmconn)
    s := string(b)
    if !strings.Contains(s, "interface-name=multinic0") || !strings.Contains(s, "address1=11.11.11.107/24") {
        t.Fatalf("nmconnection content invalid:\n%s", s)
    }
    // ensure no systemctl restart NetworkManager
    for _, c := range exec.calls { if len(c) > 0 && c[0] == "systemctl" { t.Fatalf("unexpected systemctl call: %#v", c) } }
}
