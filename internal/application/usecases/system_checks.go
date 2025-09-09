package usecases

import (
    "context"
    "strings"

    "multinic-agent/internal/domain/entities"

    "github.com/sirupsen/logrus"
)

// checkSystemInterfaceDrift는 CR 설정과 실제 시스템 인터페이스 상태를 비교합니다
func (uc *ConfigureNetworkUseCase) checkSystemInterfaceDrift(ctx context.Context, dbIface entities.NetworkInterface, interfaceName string) bool {
    // 시스템 전체에서 CR의 MAC이 존재하는지 확인 (이름 고정 X)
    foundName, err := uc.namingService.FindInterfaceNameByMAC(dbIface.MacAddress())
    if err != nil || strings.TrimSpace(foundName) == "" {
        uc.logger.WithFields(logrus.Fields{
            "interface_name": interfaceName,
            "cr_mac":         dbIface.MacAddress(),
            "error":          err,
        }).Warn("System MAC presence validation failed (not found)")
        return true
    }

    // 추가 검증: 인터페이스 상태 확인 (UP 상태면 위험함)
    if uc.isInterfaceUp(ctx, foundName) {
        uc.logger.WithFields(logrus.Fields{
            "interface_name": foundName,
            "mac_address":    strings.ToLower(dbIface.MacAddress()),
        }).Warn("Target interface is UP - potentially dangerous to modify")
        return true
    }

    uc.logger.WithFields(logrus.Fields{
        "interface_name": foundName,
        "mac_address":    strings.ToLower(dbIface.MacAddress()),
    }).Debug("System MAC presence validation passed")
    return false
}

// isInterfaceUp은 인터페이스가 UP 상태인지 확인합니다
func (uc *ConfigureNetworkUseCase) isInterfaceUp(ctx context.Context, interfaceName string) bool {
    isUp, err := uc.namingService.IsInterfaceUp(interfaceName)
    if err != nil {
        uc.logger.WithFields(logrus.Fields{
            "interface_name": interfaceName,
            "error":          err,
        }).Debug("Failed to check interface UP status, assuming safe to modify")
        return false // 에러 시 안전하게 false 반환 (처리 허용)
    }
    return isUp
}

