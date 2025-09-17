package usecases

import (
    "context"
    "fmt"
    "path/filepath"

    "multinic-agent/internal/domain/entities"
    "multinic-agent/internal/domain/interfaces"
)

// checkNeedProcessing는 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName, osType interfaces.OSType) (bool, string) {
    if osType == interfaces.OSTypeRHEL {
        return uc.checkRHELNeedProcessing(ctx, iface, interfaceName)
    }
    return uc.checkNetplanNeedProcessing(ctx, iface, interfaceName)
}

// checkRHELNeedProcessing는 RHEL 시스템에서 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkRHELNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) (bool, string) {
    configPath := uc.driftDetector.FindIfcfgFile(uc.configurer.GetConfigDir(), interfaceName.String())
    fileExists := configPath != ""

    isDrifted := false
    if fileExists { isDrifted = uc.driftDetector.IsIfcfgDrift(ctx, iface, configPath) }

    // 파일이 없거나, 드리프트가 있거나, 아직 설정되지 않은 경우 처리
    shouldProcess := !fileExists || isDrifted || iface.Status() == entities.StatusPending
    return shouldProcess, configPath
}

// checkNetplanNeedProcessing는 Ubuntu 시스템에서 인터페이스 처리 필요성을 검사합니다
func (uc *ConfigureNetworkUseCase) checkNetplanNeedProcessing(ctx context.Context, iface entities.NetworkInterface, interfaceName entities.InterfaceName) (bool, string) {
    configPath := uc.driftDetector.FindNetplanFileForInterface(uc.configurer.GetConfigDir(), interfaceName.String())
    if configPath == "" {
        // 파일이 없으면 새로 생성할 경로 설정
        configPath = filepath.Join(uc.configurer.GetConfigDir(), fmt.Sprintf("9%d-%s.yaml", interfaceName.Index(), interfaceName.String()))
    }

    // 파일이 존재하지 않거나, 드리프트가 발생했거나, 아직 설정되지 않은 경우 처리
    fileExists := uc.fileSystem.Exists(configPath)
    isDrifted := false
    if fileExists { isDrifted = uc.driftDetector.IsNetplanDrift(ctx, iface, configPath) }

    shouldProcess := !fileExists || isDrifted || iface.Status() == entities.StatusPending
    return shouldProcess, configPath
}
