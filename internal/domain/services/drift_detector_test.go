package services

import (
    "context"
    "testing"
    "time"

    "multinic-agent/internal/domain/entities"

    "github.com/stretchr/testify/assert"
    "github.com/stretchr/testify/mock"
    "github.com/sirupsen/logrus"
)

func TestDriftDetector_IsNetplanDrift_FileNotExist(t *testing.T) {
    mockFS := new(MockFileSystem)
    mockExec := new(MockCommandExecutor)
    // constructor check for container
    mockExec.On("ExecuteWithTimeout", mock.Anything, time.Second, "test", "-d", "/host").Return([]byte(""), nil)
    naming := NewInterfaceNamingService(mockFS, mockExec)
    detector := NewDriftDetector(mockFS, logrus.New(), naming)

    cfgPath := "/etc/netplan/91-multinic0.yaml"
    mockFS.On("Exists", cfgPath).Return(false).Once()

    ni, _ := entities.NewNetworkInterface(1, "aa:bb:cc:dd:ee:ff", "node1", "10.0.0.10", "10.0.0.0/24", 1500)
    drift := detector.IsNetplanDrift(context.Background(), *ni, cfgPath)
    assert.True(t, drift)
}

func TestDriftDetector_IsNetplanDrift_NoDrift(t *testing.T) {
    mockFS := new(MockFileSystem)
    mockExec := new(MockCommandExecutor)
    mockExec.On("ExecuteWithTimeout", mock.Anything, time.Second, "test", "-d", "/host").Return([]byte(""), nil)
    naming := NewInterfaceNamingService(mockFS, mockExec)
    detector := NewDriftDetector(mockFS, logrus.New(), naming)

    cfgPath := "/etc/netplan/91-multinic0.yaml"
    content := []byte(`network:
  version: 2
  ethernets:
    multinic0:
      match:
        macaddress: aa:bb:cc:dd:ee:ff
      dhcp4: false
      addresses: ["10.0.0.10/24"]
      mtu: 1500
`)
    mockFS.On("Exists", cfgPath).Return(true).Once()
    mockFS.On("ReadFile", cfgPath).Return(content, nil).Once()

    // naming.FindInterfaceNameByMAC should return a name, and IsInterfaceUp should be false to avoid system-drift
    mockExec.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "ip", "-o", "link", "show").Return([]byte("2: multinic0: ... link/ether aa:bb:cc:dd:ee:ff brd ..."), nil)
    mockExec.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "ip", "link", "show", "multinic0").Return([]byte("2: multinic0: <BROADCAST,MULTICAST> mtu 1500 qdisc noop state DOWN mode DEFAULT group default qlen 1000"), nil)

    ni, _ := entities.NewNetworkInterface(1, "aa:bb:cc:dd:ee:ff", "node1", "10.0.0.10", "10.0.0.0/24", 1500)
    drift := detector.IsNetplanDrift(context.Background(), *ni, cfgPath)
    assert.False(t, drift)
}

func TestDriftDetector_IsIfcfgDrift_NoDrift(t *testing.T) {
    mockFS := new(MockFileSystem)
    mockExec := new(MockCommandExecutor)
    mockExec.On("ExecuteWithTimeout", mock.Anything, time.Second, "test", "-d", "/host").Return([]byte(""), nil)
    naming := NewInterfaceNamingService(mockFS, mockExec)
    detector := NewDriftDetector(mockFS, logrus.New(), naming)

    cfgPath := "/etc/sysconfig/network-scripts/ifcfg-multinic0"
    content := []byte("DEVICE=multinic0\nNAME=multinic0\nTYPE=Ethernet\nONBOOT=yes\nBOOTPROTO=none\nIPADDR=10.0.0.10\nPREFIX=24\nMTU=1500\nHWADDR=aa:bb:cc:dd:ee:ff\n")
    mockFS.On("ReadFile", cfgPath).Return(content, nil).Once()

    // System checks: name found, not UP
    mockExec.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "ip", "-o", "link", "show").Return([]byte("2: multinic0: ... link/ether aa:bb:cc:dd:ee:ff brd ..."), nil)
    mockExec.On("ExecuteWithTimeout", mock.Anything, 10*time.Second, "ip", "link", "show", "multinic0").Return([]byte("state DOWN"), nil)

    ni, _ := entities.NewNetworkInterface(1, "aa:bb:cc:dd:ee:ff", "node1", "10.0.0.10", "10.0.0.0/24", 1500)
    drift := detector.IsIfcfgDrift(context.Background(), *ni, cfgPath)
    assert.False(t, drift)
}
