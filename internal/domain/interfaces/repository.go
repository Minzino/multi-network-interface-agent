package interfaces

import (
	"context"
	"multinic-agent/internal/domain/entities"
)

// NetworkInterfaceRepository는 네트워크 인터페이스 저장소 인터페이스입니다
type NetworkInterfaceRepository interface {
	// GetPendingInterfaces는 특정 노드의 설정 대기 중인 인터페이스들을 조회합니다
	GetPendingInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error)

	// GetConfiguredInterfaces는 특정 노드의 설정 완료된 인터페이스들을 조회합니다
	GetConfiguredInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error)

	// UpdateInterfaceStatus는 인터페이스의 설정 상태를 업데이트합니다
	UpdateInterfaceStatus(ctx context.Context, interfaceID int, status entities.InterfaceStatus) error

	// GetInterfaceByID는 ID로 인터페이스를 조회합니다
	GetInterfaceByID(ctx context.Context, id int) (*entities.NetworkInterface, error)

	// GetActiveInterfaces는 특정 노드의 활성 인터페이스들을 조회합니다 (삭제 감지용)
	GetActiveInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error)
	GetAllNodeInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error)
}
