# MultiNIC Agent (Controller + Job + NodeCR)

Kubernetes 네이티브 방식으로 노드별 다중 NIC를 구성합니다. Controller가 노드 OS를 자동 감지하고 OS별로 필요한 디렉토리만 마운트한 Job을 생성합니다. Agent Job은 자신의 Node CR(spec)을 읽어 네트워크를 적용하며, 상태(status)는 Controller가 업데이트합니다.

## 핵심 개념
- **CRD: MultiNicNodeConfig** (`multinic.io/v1alpha1`)
  - `spec.nodeName`: 대상 Kubernetes 노드명(= CR 이름과 동일 권장)
  - `spec.instanceId`: OpenStack Instance UUID (Node.status.nodeInfo.systemUUID와 일치)
  - `spec.interfaces[]`: `{id, macAddress, address, cidr, mtu}`
  - `status`: `{state, conditions[], interfaceStatuses[] ...}`
- **Controller**
  - CR Watch → 대상 Node 조회(OS Image/UUID) → OS별 마운트로 Job 생성
  - Job 완료/실패를 감지하여 CR `status.state`를 `Configured/Failed`로 반영
- **Agent Job**
  - `NODE_NAME <- spec.nodeName`으로 자신의 CR만 읽고 네트워크 적용
  - Ubuntu: Netplan(`/etc/netplan`), RHEL 9.4: NetworkManager keyfiles(`/etc/NetworkManager/system-connections`)

## 빠른 시작

### 1) Helm 배포 (Controller + Job 모드)
```bash
helm upgrade --install multinic deployments/helm -n multinic-system --create-namespace \
  --set controller.enabled=true \
  --set agent.dataSource=nodecr \
  --set agent.nodeCRNamespace=multinic-system \
  --set image.repository=<이미지> \
  --set image.tag=<태그> \
  --set image.pullPolicy=IfNotPresent
```

※ Helm 3는 `deployments/helm/crds/*`의 CRD를 자동 설치합니다(업그레이드시 CRD 변경은 수동 적용 필요).\
컨트롤러가 런타임에 Job을 생성하므로, Helm 차트는 기본적으로 Job 리소스를 설치하지 않습니다(`job.install=false`).

### 2) 샘플 CR 적용(viola2-biz-* 노드)
```bash
kubectl apply -n multinic-system -f deployments/crds/samples/
```

## 배포 구성
- 기본값: Controller enabled, DaemonSet 템플릿 포함(필요 시 job.enabled=true로 Job 경로 사용 권장)
- Controller 실행 모드: 기본 `watch` (Informer 기반). `CONTROLLER_MODE=poll`로 폴링 전환 가능
- 이미지: `Dockerfile`에서 에이전트(`multinic-agent`)와 컨트롤러(`multinic-controller`) 동시 빌드

## 환경 변수
- Agent
  - `DATA_SOURCE=nodecr` (Kube API에서 Node CR 읽기)
  - `NODE_CR_NAMESPACE` (기본 multinic-system)
  - `NODE_NAME` (Downward API: `spec.nodeName`)
- Controller
  - `CONTROLLER_NAMESPACE`(기본: Pod 네임스페이스)
  - `NODE_CR_NAMESPACE`(CR 조회 네임스페이스)
  - `AGENT_IMAGE`(Job에 사용할 에이전트 이미지)
  - `POLL_INTERVAL`(poll 모드 주기)
  - `CONTROLLER_MODE=watch|poll`

## CRD 예시
```yaml
apiVersion: multinic.io/v1alpha1
kind: MultiNicNodeConfig
metadata:
  name: viola2-biz-worker01
  namespace: multinic-system
  labels:
    multinic.io/node-name: viola2-biz-worker01
    multinic.io/instance-id: b4975c5f-50bb-479f-9e7b-a430815ae852
spec:
  nodeName: viola2-biz-worker01
  instanceId: b4975c5f-50bb-479f-9e7b-a430815ae852
  interfaces:
    - id: 1
      macAddress: fa:16:3e:1c:1a:6e
      address: 11.11.11.37
      cidr: 11.11.11.0/24
      mtu: 1450
```

## OS 지원
- Ubuntu: Netplan(`/etc/netplan`)로 설정 파일 생성 및 `netplan try/apply`
- RHEL 9.4: NetworkManager keyfiles(`/etc/NetworkManager/system-connections/*.nmconnection`) 사용(어댑터 구현 진행 예정)
- Host root 마운트 불필요(노드 OS 감지는 `Node.status.nodeInfo.osImage`)

## 보안/RBAC
- 최소 권한:
  - CRD 읽기/상태 패치: `multinicnodeconfigs`, `multinicnodeconfigs/status`
  - Job 생성/조회: `batch/jobs`
- Agent는 네트워크 구성에 필요한 권한(privileged, `NET_ADMIN`, `SYS_ADMIN`)만 보유
- OpenStack 매핑 검증: `spec.instanceId` ↔ `Node.systemUUID` 불일치 시 Job 생성 차단

## 트러블슈팅
- CR 상태가 `Failed`인 경우
  - Job 로그 확인: `kubectl logs -n multinic-system job/<job-name>`
  - Node UUID 확인: `kubectl get node <node> -o jsonpath='{.status.nodeInfo.systemUUID}'`
  - `spec.instanceId`와 일치하는지 점검
- OS 인식 오류 시: 컨트롤러 로그에서 `osImage` 확인
- 네임스페이스 불일치: `NODE_CR_NAMESPACE`와 CR이 위치한 네임스페이스 일치 필요

## 개발/테스트
```bash
go test ./...    # 단위 테스트 전체
```

프로젝트 구조
```
multinic-agent/
├── cmd/agent/            # Agent 바이너리
├── cmd/controller/       # Controller 바이너리
├── internal/             # Clean Architecture 레이어별 코드
│   ├── controller        # Reconciler/Watcher/Service/JobFactory
│   ├── application       # Use cases
│   ├── domain            # Entities/Interfaces
│   └── infrastructure    # Adapters (OS detector, K8s, Network, etc.)
├── deployments/
│   ├── crds/             # CRD와 샘플 CR
│   └── helm/             # Helm 차트(Controller/Job/DS/RBAC)
└── scripts/              # 배포 스크립트
```
