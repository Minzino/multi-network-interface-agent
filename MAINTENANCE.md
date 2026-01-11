# Multinic Agent 유지보수 가이드

## 1. 목적

- Biz 클러스터에 생성된 MultiNicNodeConfig CR을 감시합니다.
- CR 기준으로 노드에 멀티 NIC를 구성하는 Job을 생성합니다.
- Job 결과를 CR status로 반영하고, 실패 시 원인을 기록합니다.

## 2. 구성 요소

- Controller: `internal/controller/reconciler.go`
- Watcher: `internal/controller/watcher.go`
- CRD 샘플: `deployments/crds/samples/`
- Helm 차트: `deployments/helm`

## 3. 입력값(필수/권장)

필수:
- MultiNicNodeConfig CR
  - `spec.nodeName` (없으면 `metadata.name` 사용)
  - `spec.instanceId` (노드 UUID 검증용)
  - `spec.interfaces[]` (macAddress/address/cidr/mtu)

권장:
- `spec.interfaces[].name` (`multinic0` ~ `multinic9`)
- `spec.interfaces[].id` (0~9)

## 4. 동작 흐름(요약)

1) CR add/update 이벤트 수신
2) 노드 정보 조회 및 instanceId 매칭 검증
3) Job 생성 (nodeName + generation 기반)
4) Job 완료 시 CR status 업데이트
5) 실패 시 종료 메시지 요약 적용

## 5. Job 처리 정책

- Job 이름: `multinic-agent-<nodeName>-g<generation>`
- 동일 generation의 Job이 이미 있으면 재생성하지 않습니다.
- 완료된 Job은 TTL 또는 지연 삭제로 정리됩니다.

## 6. CR 상태 업데이트

- `status.state`: InProgress/Configured/Failed
- `status.conditions`: InProgress/Ready/Failed 등
- `status.interfaceStatuses`: 인터페이스별 상태
- `status.observedGeneration`: 마지막 처리한 generation

## 7. 배포/업데이트 절차

### 7.1 이미지 빌드

```sh
nerdctl build -t <registry>/multinic-agent:<tag> .
```

### 7.2 Helm 배포

```sh
helm upgrade --install multinic-agent deployments/helm \
  -n multinic-system --create-namespace \
  --set image.repository=<registry>/multinic-agent \
  --set image.tag=<tag>
```

## 8. 장애 대응

- CR Validation 실패
  - interfaces[] 누락/필수 필드 확인
- instanceId mismatch
  - VM UUID와 노드 SystemUUID 일치 여부 확인
- Job 생성 실패
  - RBAC, ServiceAccount, 이미지 풀 권한 확인
- 인터페이스 미구성
  - 노드 NIC 존재 여부 및 macAddress 일치 확인

## 9. 로그 확인

```sh
kubectl logs -n multinic-system deployment/multinic-agent-controller
```

## 10. 인계 포인트

- 핵심 로직: `internal/controller/reconciler.go`
- 이벤트 처리: `internal/controller/watcher.go`
- CR 예시: `deployments/crds/samples/`
