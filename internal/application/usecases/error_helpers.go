package usecases

import (
    "context"

    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/errors"

    "github.com/sirupsen/logrus"
)

// handleInterfaceError는 인터페이스 처리 에러를 기록합니다
func (uc *ConfigureNetworkUseCase) handleInterfaceError(operation string, interfaceID int, macAddress string, err error) {
    fields := logrus.Fields{
        "operation":    operation,
        "interface_id": interfaceID,
        "mac_address":  macAddress,
        "error":        err,
    }

    // 에러 타입에 따른 로그 레벨 조정
    switch {
    case errors.IsValidationError(err):
        uc.logger.WithFields(fields).Warn("Validation error")
    case errors.IsNetworkError(err):
        uc.logger.WithFields(fields).Error("Network error")
    case errors.IsTimeoutError(err):
        uc.logger.WithFields(fields).Error("Timeout error")
    default:
        uc.logger.WithFields(fields).Error("Operation failed")
    }
}

// handleProcessingError는 인터페이스 처리 중 발생한 에러를 처리합니다
func (uc *ConfigureNetworkUseCase) handleProcessingError(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName, err error) {
    uc.logger.WithFields(logrus.Fields{
        "interface_id":   iface.ID(),
        "interface_name": interfaceName.String(),
        "error_type":     uc.getErrorType(err),
        "error":          err,
    }).Error("Failed to configure/sync interface")

    // 실패 상태로 업데이트
    if updateErr := uc.repository.UpdateInterfaceStatus(ctx, iface.ID(), entities.StatusFailed); updateErr != nil {
        uc.logger.WithError(updateErr).Error("Failed to update interface status")
    }
}

// getErrorType는 에러 타입을 반환합니다
func (uc *ConfigureNetworkUseCase) getErrorType(err error) string {
    switch {
    case errors.IsValidationError(err):
        return "validation"
    case errors.IsNetworkError(err):
        return "network"
    case errors.IsTimeoutError(err):
        return "timeout"
    case errors.IsSystemError(err):
        return "system"
    default:
        return "unknown"
    }
}

