package entities

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNetworkInterface_ConstructAndValidate(t *testing.T) {
    t.Run("유효한 인터페이스", func(t *testing.T) {
        ni, err := NewNetworkInterface(1, "00:11:22:33:44:55", "test-node", "1.1.1.1", "1.1.1.0/24", 1500)
        require.NoError(t, err)
        require.NoError(t, ni.Validate())
    })

    t.Run("잘못된 MAC 주소 형식", func(t *testing.T) {
        _, err := NewNetworkInterface(1, "invalid-mac", "test-node", "1.1.1.1", "1.1.1.0/24", 1500)
        assert.Error(t, err)
    })

    t.Run("빈 노드 이름", func(t *testing.T) {
        _, err := NewNetworkInterface(1, "00:11:22:33:44:55", "", "1.1.1.1", "1.1.1.0/24", 1500)
        assert.Error(t, err)
    })

    t.Run("다양한 MAC 주소 형식 - 콜론", func(t *testing.T) {
        ni, err := NewNetworkInterface(1, "aa:bb:cc:dd:ee:ff", "test-node", "1.1.1.1", "1.1.1.0/24", 1500)
        require.NoError(t, err)
        require.NoError(t, ni.Validate())
    })

    t.Run("다양한 MAC 주소 형식 - 대시", func(t *testing.T) {
        ni, err := NewNetworkInterface(1, "AA-BB-CC-DD-EE-FF", "test-node", "1.1.1.1", "1.1.1.0/24", 1500)
        require.NoError(t, err)
        require.NoError(t, ni.Validate())
    })

    t.Run("명시적 인터페이스 이름", func(t *testing.T) {
        ni, err := NewNetworkInterfaceWithName(1, "multinic0", "00:11:22:33:44:55", "test-node", "1.1.1.1", "1.1.1.0/24", 1500)
        require.NoError(t, err)
        assert.Equal(t, "multinic0", ni.InterfaceName())
        assert.True(t, ni.HasExplicitName())
    })
}

func TestNetworkInterface_StatusMethods(t *testing.T) {
    t.Run("Status 전이", func(t *testing.T) {
        ni, err := NewNetworkInterface(1, "00:11:22:33:44:55", "node", "1.1.1.1", "1.1.1.0/24", 1500)
        require.NoError(t, err)
        assert.True(t, ni.IsPending())
        ni.MarkAsConfigured()
        assert.True(t, ni.IsConfigured())
        ni.MarkAsFailed()
        assert.True(t, ni.IsFailed())
    })
}

func TestInterfaceName_NewInterfaceName(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantError bool
	}{
		{
			name:      "유효한 인터페이스 이름 - multinic0",
			input:     "multinic0",
			wantError: false,
		},
		{
			name:      "유효한 인터페이스 이름 - multinic9",
			input:     "multinic9",
			wantError: false,
		},
		{
			name:      "잘못된 인터페이스 이름 - multinic10",
			input:     "multinic10",
			wantError: true,
		},
		{
			name:      "잘못된 인터페이스 이름 - eth0",
			input:     "eth0",
			wantError: true,
		},
		{
			name:      "잘못된 인터페이스 이름 - 빈 문자열",
			input:     "",
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
            result, err := NewInterfaceName(tt.input)
            if tt.wantError {
                assert.Error(t, err)
            } else {
                assert.NoError(t, err)
                assert.Equal(t, tt.input, result.String())
            }
		})
	}
}

func TestInterfaceName_String(t *testing.T) {
	name, err := NewInterfaceName("multinic5")
	require.NoError(t, err)

	assert.Equal(t, "multinic5", name.String())
}

func TestMacAddressValidation(t *testing.T) {
	tests := []struct {
		name      string
		macAddr   string
		wantValid bool
	}{
		{"유효한 MAC - 소문자 콜론", "00:11:22:33:44:55", true},
		{"유효한 MAC - 대문자 콜론", "AA:BB:CC:DD:EE:FF", true},
		{"유효한 MAC - 대시", "00-11-22-33-44-55", true},
		{"유효한 MAC - 혼합", "aA:bB:cC:dD:eE:fF", true},
		{"잘못된 MAC - 짧음", "00:11:22:33:44", false},
		{"잘못된 MAC - 길음", "00:11:22:33:44:55:66", false},
		{"잘못된 MAC - 잘못된 문자", "00:11:22:33:44:GG", false},
		{"잘못된 MAC - 형식 오류", "00112233445", false},
		{"빈 문자열", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidMacAddress(tt.macAddr)
			assert.Equal(t, tt.wantValid, result)
		})
	}
}

func TestInterfaceNameValidation(t *testing.T) {
	tests := []struct {
		name      string
		ifaceName string
		wantValid bool
	}{
		{"유효한 이름 - multinic0", "multinic0", true},
		{"유효한 이름 - multinic1", "multinic1", true},
		{"유효한 이름 - multinic9", "multinic9", true},
		{"잘못된 이름 - multinic10", "multinic10", false},
		{"잘못된 이름 - eth0", "eth0", false},
		{"잘못된 이름 - ens33", "ens33", false},
		{"잘못된 이름 - multinica", "multinica", false},
		{"빈 문자열", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isValidInterfaceName(tt.ifaceName)
			assert.Equal(t, tt.wantValid, result)
		})
	}
}
