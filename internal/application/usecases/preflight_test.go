package usecases

import (
    "context"
    "fmt"
    "testing"
    "time"

    "os"
    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/domain/services"

    "github.com/sirupsen/logrus"
    "github.com/stretchr/testify/require"
)

// Minimal stubs for preflight option test
type pfRepo struct{ ifaces []entities.NetworkInterface }
func (s *pfRepo) GetAllNodeInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return s.ifaces, nil }
func (s *pfRepo) UpdateInterfaceStatus(ctx context.Context, id int, status entities.InterfaceStatus) error { return nil }
func (s *pfRepo) GetPendingInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }
func (s *pfRepo) GetConfiguredInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }
func (s *pfRepo) GetInterfaceByID(ctx context.Context, id int) (*entities.NetworkInterface, error) { return nil, nil }
func (s *pfRepo) GetActiveInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }

type pfFS struct{}
func (pfFS) ReadFile(string) ([]byte, error) { return []byte{}, nil }
func (pfFS) WriteFile(string, []byte, os.FileMode) error { return nil }
func (pfFS) Exists(string) bool { return false }
func (pfFS) MkdirAll(string, os.FileMode) error { return nil }
func (pfFS) Remove(string) error { return nil }
func (pfFS) ListFiles(string) ([]string, error) { return []string{}, nil }

type pfExec struct{}
func (pfExec) Execute(ctx context.Context, cmd string, args ...string) ([]byte, error) {
    return pfExec{}.ExecuteWithTimeout(ctx, time.Second, cmd, args...)
}
func (pfExec) ExecuteWithTimeout(ctx context.Context, _ time.Duration, cmd string, args ...string) ([]byte, error) {
    if cmd == "test" && len(args) >= 2 && args[0] == "-d" && args[1] == "/host" { return []byte{}, fmt.Errorf("not in container") }
    if cmd == "ip" {
        if len(args) >= 3 && args[0] == "addr" && args[1] == "show" { return []byte(""), fmt.Errorf("Device does not exist") }
        if len(args) >= 3 && args[0] == "link" && args[1] == "show" { return []byte("state UP"), nil }
        if len(args) >= 3 && args[0] == "-o" && args[1] == "link" && args[2] == "show" {
            // single eth0 with given MAC
            return []byte("2: eth0: <BROADCAST,MULTICAST> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000    link/ether 02:00:00:00:00:01 brd ff:ff:ff:ff:ff:ff"), nil
        }
    }
    return []byte(""), nil
}

type pfOS struct{}
func (pfOS) DetectOS() (interfaces.OSType, error) { return interfaces.OSTypeUbuntu, nil }

type pfCfg struct{}
func (pfCfg) GetConfigDir() string { return "/etc/netplan" }
func (pfCfg) Validate(ctx context.Context, name entities.InterfaceName) error { return nil }
func (pfCfg) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error { return nil }

type pfRB struct{}
func (pfRB) Rollback(ctx context.Context, name string) error { return nil }

func TestPreflight_BlockIfUP_Option(t *testing.T) {
    ni, err := entities.NewNetworkInterface(1, "02:00:00:00:00:01", "node", "10.0.0.2", "10.0.0.0/24", 1500)
    require.NoError(t, err)

    repo := &pfRepo{ifaces: []entities.NetworkInterface{*ni}}
    fs := pfFS{}
    ex := pfExec{}
    osd := pfOS{}
    cfg := pfCfg{}
    rb := pfRB{}
    naming := services.NewInterfaceNamingService(fs, ex)
    logger := logrus.New(); logger.SetLevel(logrus.ErrorLevel)

    uc := NewConfigureNetworkUseCaseWithDetector(
        repo, cfg, rb, naming, fs, osd, logger,
        1,
        services.NewDriftDetector(fs, logger, naming),
        time.Second,
        0,
        2.0,
    )

    out, err := uc.Execute(context.Background(), ConfigureNetworkInput{NodeName: "node"})
    require.NoError(t, err)
    require.NotNil(t, out)
    require.Equal(t, 1, out.FailedCount)
}
