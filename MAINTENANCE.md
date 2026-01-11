# Multinic Agent 코드 유지보수 가이드

이 문서는 운영이 아니라 **코드 유지보수 관점**에서 폴더/파일 역할과 수정 포인트를 정리합니다.

## 1. 디렉터리 구조

- `cmd/`
  - `agent/`: 노드에서 실행되는 에이전트 엔트리포인트.
  - `controller/`: CR 감시/Job 생성 컨트롤러 엔트리포인트.
- `internal/controller/`
  - CR → Job 변환 및 상태 갱신 로직.
- `internal/application/`
  - 폴링/유스케이스 계층 (usecases, polling).
- `internal/domain/`
  - 엔티티/서비스/인터페이스/에러/상수 정의.
- `internal/infrastructure/`
  - 실제 구현체 (네트워크/컨테이너/헬스/메트릭/설정/퍼시스턴스/어댑터).
- `deployments/`
  - Helm 차트 및 CR 샘플.
- `docs/`
  - 추가 문서.

## 2. 핵심 파일/흐름

- `internal/controller/reconciler.go`
  - MultiNicNodeConfig 기준 Job 생성 및 CR 상태 갱신.
- `internal/controller/watcher.go`
  - CR/Job/Pod 인포머 이벤트 처리.
- `internal/controller/jobfactory.go`
  - OS별 Job 스펙 빌더.
- `cmd/controller/main.go`
  - 컨트롤러 시작, 모드(watch/poll) 선택.
- `cmd/agent/main.go`
  - 실제 NIC 구성 로직 진입점.

## 3. 자주 수정되는 포인트

- Job 스펙 변경
  - `internal/controller/jobfactory.go`
  - 필요한 env/volume/privilege 수정

- CR 상태 포맷 변경
  - `internal/controller/reconciler.go` 상태 업데이트 부분

- 에이전트 동작 변경
  - `internal/application/` 및 `internal/infrastructure/`
  - 실제 NIC 설정 로직은 infrastructure/network 쪽을 확인

## 4. 빌드/테스트

```sh
go test ./...
```

## 5. 배포 관련 파일(코드 관점)

- Helm: `deployments/helm/`
- CR 샘플: `deployments/crds/samples/`

## 6. 인계 포인트

- 핵심 로직: `internal/controller/reconciler.go`
- 이벤트 처리: `internal/controller/watcher.go`
- Job 빌더: `internal/controller/jobfactory.go`
