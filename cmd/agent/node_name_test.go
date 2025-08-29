package main

import (
    "os"
    "testing"

    "github.com/sirupsen/logrus"
    "github.com/stretchr/testify/assert"
)

func TestResolveNodeName_UsesEnvFirst(t *testing.T) {
    t.Setenv("NODE_NAME", "k8s-worker-01")
    logger := logrus.New()
    got, err := resolveNodeName(logger)
    assert.NoError(t, err)
    assert.Equal(t, "k8s-worker-01", got)
}

func TestCleanHostnameDomainSuffix(t *testing.T) {
    // no dot
    assert.Equal(t, "node-01", cleanHostnameDomainSuffix("node-01"))
    // with dot
    assert.Equal(t, "node-01", cleanHostnameDomainSuffix("node-01.example.local"))
}

func TestResolveNodeName_FallbackHostname(t *testing.T) {
    // Ensure env is not set
    os.Unsetenv("NODE_NAME")
    os.Unsetenv("MY_NODE_NAME")
    // We cannot control os.Hostname() easily, so just ensure it doesn't error
    logger := logrus.New()
    _, err := resolveNodeName(logger)
    assert.NoError(t, err)
}

