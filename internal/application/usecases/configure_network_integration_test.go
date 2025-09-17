package usecases

import (
    "context"
    "fmt"
    "os"
    "sync/atomic"
    "testing"
    "time"

    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/interfaces"
    "multinic-agent/internal/domain/services"

    "github.com/sirupsen/logrus"
    "github.com/stretchr/testify/require"
    "strings"
)

// --- Stubs for integration-style tests ---

type stubRepo struct {
    ifaces []entities.NetworkInterface
    // record failed updates by id
    failedUpdates atomic.Int32
}

func (s *stubRepo) GetAllNodeInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) {
    return s.ifaces, nil
}
func (s *stubRepo) UpdateInterfaceStatus(ctx context.Context, id int, status entities.InterfaceStatus) error {
    if status == entities.StatusFailed { s.failedUpdates.Add(1) }
    return nil
}
func (s *stubRepo) GetPendingInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }
func (s *stubRepo) GetConfiguredInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }
func (s *stubRepo) GetInterfaceByID(ctx context.Context, id int) (*entities.NetworkInterface, error) { return nil, nil }
func (s *stubRepo) GetActiveInterfaces(ctx context.Context, node string) ([]entities.NetworkInterface, error) { return nil, nil }

type stubFS struct{}
func (s *stubFS) ReadFile(path string) ([]byte, error) { return []byte{}, nil }
func (s *stubFS) WriteFile(path string, data []byte, perm os.FileMode) error { return nil }
func (s *stubFS) Exists(path string) bool { return false }
func (s *stubFS) MkdirAll(path string, perm os.FileMode) error { return nil }
func (s *stubFS) Remove(path string) error { return nil }
func (s *stubFS) ListFiles(path string) ([]string, error) { return []string{}, nil }

type stubExec struct{ macs []string }
func (s *stubExec) Execute(ctx context.Context, cmd string, args ...string) ([]byte, error) {
    return s.ExecuteWithTimeout(ctx, time.Second, cmd, args...)
}
func (s *stubExec) ExecuteWithTimeout(ctx context.Context, _ time.Duration, cmd string, args ...string) ([]byte, error) {
    // minimal responses for naming/validation paths
    if cmd == "test" && len(args) >= 2 && args[0] == "-d" && args[1] == "/host" {
        return []byte{}, fmt.Errorf("not in container")
    }
    if cmd == "nmcli" { return []byte(""), nil }
    if cmd == "ip" {
        if len(args) >= 3 && args[0] == "addr" && args[1] == "show" {
            // ReserveNamesForInterfaces expects non-existence for multinicX candidates.
            // Only eth0 is assumed to exist with a MAC.
            ifName := args[2]
            if ifName == "eth0" && len(s.macs) > 0 {
                return []byte(fmt.Sprintf("link/ether %s brd ff:ff:ff:ff:ff:ff", s.macs[0])), nil
            }
            return []byte(""), fmt.Errorf("Device does not exist")
        }
        if len(args) >= 3 && args[0] == "-o" && args[1] == "link" && args[2] == "show" {
            out := ""
            for i, mac := range s.macs {
                // After configuration, all interfaces should be renamed to multinic* and be UP
                if i > 0 { out += "\n" }
                out += fmt.Sprintf("%d: multinic%d: <BROADCAST,MULTICAST,UP,LOWER_UP> mtu 1500 qdisc fq_codel state UP mode DEFAULT group default qlen 1000\\    link/ether %s brd ff:ff:ff:ff:ff:ff", i+2, i, mac)
            }
            return []byte(out), nil
        }
        if len(args) >= 3 && args[0] == "link" && args[1] == "show" {
            // Preflight should see eth0 as DOWN; validation should see multinic* as UP
            if len(args) >= 3 {
                ifName := args[2]
                if ifName == "eth0" { return []byte("state DOWN"), nil }
                if strings.HasPrefix(ifName, "multinic") { return []byte("state UP"), nil }
            }
            return []byte("state DOWN"), nil
        }
    }
    return []byte(""), nil
}

type stubOS struct{}
func (s *stubOS) DetectOS() (interfaces.OSType, error) { return interfaces.OSTypeUbuntu, nil }

type stubConfigurer struct {
    sleep time.Duration
    // firstFailID will fail once then succeed
    firstFailID int
    attempts    atomic.Int32
    current     atomic.Int32
    maxObserved atomic.Int32
}

func (s *stubConfigurer) GetConfigDir() string { return "/etc/netplan" }
func (s *stubConfigurer) Validate(ctx context.Context, name entities.InterfaceName) error { return nil }
func (s *stubConfigurer) Configure(ctx context.Context, iface entities.NetworkInterface, name entities.InterfaceName) error {
    // concurrency count
    cur := s.current.Add(1)
    for {
        max := s.maxObserved.Load()
        if cur > max {
            if s.maxObserved.CompareAndSwap(max, cur) { break }
            continue
        }
        break
    }
    defer s.current.Add(-1)
    if s.sleep > 0 { time.Sleep(s.sleep) }
    if iface.ID() == s.firstFailID {
        // fail first attempt only
        if s.attempts.Add(1) == 1 {
            return fmt.Errorf("temp error")
        }
    }
    return nil
}

type stubRollbacker struct{}
func (s *stubRollbacker) Rollback(ctx context.Context, name string) error { return nil }

// --- Tests ---

func TestConfigureNetwork_ConcurrencyCap(t *testing.T) {
    // Prepare 10 interfaces
    var ifaces []entities.NetworkInterface
    for i := 0; i < 10; i++ {
        mac := fmt.Sprintf("02:00:00:00:00:%02x", i)
        ni, err := entities.NewNetworkInterface(i, mac, "node", "10.0.0.1", "10.0.0.0/24", 1500)
        require.NoError(t, err)
        ifaces = append(ifaces, *ni)
    }

    repo := &stubRepo{ifaces: ifaces}
    cfg := &stubConfigurer{sleep: 30 * time.Millisecond}
    rb := &stubRollbacker{}
    fs := &stubFS{}
    // prepare MAC list for executor to report
    var macs []string
    for _, it := range ifaces { macs = append(macs, it.MacAddress()) }
    ex := &stubExec{macs: macs}
    osd := &stubOS{}
    naming := services.NewInterfaceNamingService(fs, ex)
    logger := logrus.New(); logger.SetLevel(logrus.DebugLevel)

    uc := NewConfigureNetworkUseCaseWithDetector(
        repo, cfg, rb, naming, fs, osd, logger,
        3, // MaxConcurrentTasks
        services.NewDriftDetector(fs, logger, naming),
        2*time.Second, // op timeout
        1, // maxRetries
        2.0, // backoff multiplier
    )

    out, err := uc.Execute(context.Background(), ConfigureNetworkInput{NodeName: "node"})
    require.NoError(t, err)
    require.Equal(t, 10, out.TotalCount)
    require.Equal(t, 0, out.FailedCount)
    require.Equal(t, 10, out.ProcessedCount)
    // Verify observed concurrency cap
    require.LessOrEqual(t, int(cfg.maxObserved.Load()), 3)
    // No failure status updates should be recorded
    require.Equal(t, int32(0), repo.failedUpdates.Load())
}

func TestConfigureNetwork_RetryEventuallySucceeds(t *testing.T) {
    ni, err := entities.NewNetworkInterface(1, "02:00:00:00:00:01", "node", "10.0.0.2", "10.0.0.0/24", 1500)
    require.NoError(t, err)
    repo := &stubRepo{ifaces: []entities.NetworkInterface{*ni}}
    cfg := &stubConfigurer{sleep: 10 * time.Millisecond, firstFailID: 1}
    rb := &stubRollbacker{}
    fs := &stubFS{}
    ex := &stubExec{macs: []string{"02:00:00:00:00:01"}}
    osd := &stubOS{}
    naming := services.NewInterfaceNamingService(fs, ex)
    logger := logrus.New(); logger.SetLevel(logrus.DebugLevel)

    uc := NewConfigureNetworkUseCaseWithDetector(
        repo, cfg, rb, naming, fs, osd, logger,
        1, // MaxConcurrentTasks
        services.NewDriftDetector(fs, logger, naming),
        time.Second,
        2, // allow at least one retry
        2.0,
    )

    out, err := uc.Execute(context.Background(), ConfigureNetworkInput{NodeName: "node"})
    require.NoError(t, err)
    require.Equal(t, 1, out.TotalCount)
    require.Equal(t, 0, out.FailedCount)
    require.Equal(t, 1, out.ProcessedCount)
    // Ensure we did not mark as failed due to eventual success
    require.Equal(t, int32(0), repo.failedUpdates.Load())
    // Ensure retries were actually attempted
    require.GreaterOrEqual(t, int(cfg.attempts.Load()), 1)
}
