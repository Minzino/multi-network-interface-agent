# MultiNIC Agent 아키텍처 재설계 계획

## 세션 요약

이전 세션에서 현재 클러스터 전체 CRD 방식의 근본적인 한계점들을 분석하고, 노드별 CRD 기반의 새로운 아키텍처를 설계했습니다.

## 현재 아키텍처의 문제점

### 1. 확장성 문제 (Scale Issue)
- **etcd 용량 한계**: 단일 CR 크기가 1.5MB 제한에 근접
  - 1000 노드 × 5 인터페이스 = 5MB+ 단일 객체
  - OpenStack 메타데이터까지 포함하면 더 큰 용량

### 2. API 부담 (VIOLA API Burden)
- 외부 VIOLA API가 전체 클러스터 상태를 수집해서 거대한 CR 생성 필요
- 네트워크 대역폭과 API 응답 시간에 부정적 영향

### 3. 동시성 충돌 (Concurrency Conflicts)
- 여러 Job이 동일한 CR을 동시에 업데이트 → Resource Version 충돌
- Controller는 전체 노드를 다시 스케줄링해야 함 (부분 실패 시)

### 4. 보안 위반 (Security Violation)
- Job Pod들이 클러스터 전체 CR 수정 권한 필요
- 최소 권한 원칙(Principle of Least Privilege) 위반

### 5. 아키텍처 안티패턴
- Job이 직접 CR 상태를 업데이트 → Controller 패턴 위배
- 상태 관리 책임이 분산되어 일관성 보장 어려움

## 새로운 노드 기반 아키텍처

### 핵심 설계 원칙
1. **단일 책임 원칙**: 각 구성 요소가 명확한 역할 담당
2. **최소 권한**: 각 구성 요소가 필요한 최소한의 권한만 보유
3. **확장성**: 노드 수에 비례하여 선형적으로 확장
4. **동시성 안전**: 동시 업데이트로 인한 충돌 방지

### CRD 구조 변경: MultiNicNodeConfig

```yaml
apiVersion: multinic.io/v1
kind: MultiNicNodeConfig
metadata:
  name: "worker-node-01"  # 노드명과 일치
  namespace: multinic-system
  labels:
    multinic.io/node-name: "worker-node-01"
    multinic.io/instance-id: "i-0123456789abcdef0"
spec:
  providerId: "0c497169-a104-4448-afde-f27b79fca904"
  nodeName: "worker-node-01"
  instanceId: "i-0123456789abcdef0"
  specHash: "abc123def456"
  interfaces:
  - portId: "port-001-worker-01"
    id: 1
    macAddress: "02:00:00:00:01:01"
    address: "192.168.100.10"
    cidr: "192.168.100.10/24"
    mtu: 1500
  - portId: "port-002-worker-01" 
    id: 2
    macAddress: "02:00:00:00:01:02"
    address: "192.168.200.10"
    cidr: "192.168.200.10/24"
    mtu: 1500
status:
  observedGeneration: 1
  observedSpecHash: "abc123def456"
  lastProcessed: "2025-01-21T10:32:15Z"
  lastJobName: "multinic-agent-worker-node-01-20250121103200"
  state: "Configured"  # Pending|InProgress|Configured|Failed
  conditions:
  - type: "Ready"
    status: "True"
    lastTransitionTime: "2025-01-21T10:32:15Z"
    reason: "AllInterfacesConfigured"
    message: "All 2 interfaces configured successfully"
  interfaceStatuses:
  - id: 1
    macAddress: "02:00:00:00:01:01"
    status: "Configured"
    lastConfigured: "2025-01-21T10:32:10Z"
    message: "Interface configured successfully"
  - id: 2
    macAddress: "02:00:00:00:01:02"
    status: "Configured"
    lastConfigured: "2025-01-21T10:32:12Z"
    message: "Interface configured successfully"
```

### 책임 분리 (Separation of Responsibilities)

#### 1. VIOLA API (External)
- **역할**: OpenStack 인스턴스 모니터링 및 노드별 CR 생성/업데이트
- **권한**: MultiNicNodeConfig 생성/수정 (spec 필드만)
- **동작**: 
  - OpenStack API에서 인스턴스별 네트워크 구성 조회
  - 각 인스턴스(노드)별로 개별 MultiNicNodeConfig CR 생성
  - spec.specHash로 변경사항 감지

#### 2. Controller (Internal)
- **역할**: CR 변경사항 감시 및 Job 스케줄링, 상태 관리
- **권한**: MultiNicNodeConfig 읽기/상태 업데이트, Job 생성/관리
- **동작**:
  - 모든 MultiNicNodeConfig CR 감시
  - spec 변경 시 해당 노드에만 Job 스케줄링
  - Job 완료 후 CR 상태 업데이트 (status 필드만)
  - 기존 Job이 실행 중이면 완료 대기 또는 취소

#### 3. Agent Job (Internal)
- **역할**: 대상 노드에서 실제 네트워크 인터페이스 구성
- **권한**: 노드 레벨 네트워크 구성, 자신의 MultiNicNodeConfig 읽기만
- **동작**:
  - 해당 노드의 MultiNicNodeConfig spec 읽기
  - 네트워크 인터페이스 구성 (netplan/ifcfg)
  - 결과를 Controller에 보고 (ConfigMap 또는 로그)
  - **CR 직접 수정하지 않음**

### 새로운 워크플로우

```
1. VIOLA API: OpenStack 감지 → MultiNicNodeConfig CR 생성/업데이트
2. Controller: CR 변경 감지 → 해당 노드에 Job 스케줄링
3. Agent Job: 노드에서 네트워크 구성 실행 → 결과 보고
4. Controller: Job 결과 수집 → MultiNicNodeConfig 상태 업데이트
```

## 3단계 마이그레이션 계획

### Phase 1: 병렬 운영 (Dual Mode)
- 기존 MultiNicBizConfig와 새로운 MultiNicNodeConfig 동시 지원
- Controller가 두 타입 모두 처리
- 기존 VIOLA API는 계속 동작

### Phase 2: 점진적 전환 (Gradual Migration)
- VIOLA API 업데이트하여 노드별 CR 생성 방식으로 변경
- 기존 클러스터 전체 CR은 읽기 전용으로 유지
- 새로운 노드는 MultiNicNodeConfig만 사용

### Phase 3: 완전 전환 (Complete Migration)
- MultiNicBizConfig 지원 제거
- Controller 코드 정리
- 문서 업데이트

## 기대 효과

### 1. 확장성 개선
- 노드당 ~5KB CR → 1000 노드도 etcd 용량 문제 없음
- 선형적 확장성 확보

### 2. 성능 향상
- 병렬 처리: 여러 노드 동시 구성 가능
- 부분 실패 격리: 한 노드 실패가 다른 노드에 영향 없음
- 네트워크 효율성: 필요한 노드 정보만 전송

### 3. 보안 강화
- Job Pod는 자신의 노드 정보만 읽기 권한
- Controller만 전체 클러스터 관리 권한
- 최소 권한 원칙 준수

### 4. 운영성 개선
- 노드별 독립적 문제 해결
- 디버깅 및 모니터링 용이
- 롤백 시 영향 범위 최소화

## 주요 구현 작업

### 1. CRD 정의
- [ ] MultiNicNodeConfig CRD 생성
- [ ] 스키마 검증 규칙 추가
- [ ] 인덱스 및 라벨 정의

### 2. Controller 수정
- [ ] 노드별 CR 감시 로직 추가
- [ ] Job 스케줄링 로직 수정
- [ ] 상태 관리 로직 구현
- [ ] 동시성 제어 메커니즘

### 3. Agent 수정
- [ ] 노드별 CR 읽기 로직
- [ ] 결과 보고 메커니즘
- [ ] 오류 처리 개선

### 4. RBAC 업데이트
- [ ] 세분화된 권한 정의
- [ ] 최소 권한 원칙 적용
- [ ] 보안 검토

### 5. 배포 및 테스트
- [ ] Helm 차트 업데이트
- [ ] 마이그레이션 도구 개발
- [ ] E2E 테스트 작성
- [ ] 성능 테스트

## 다음 세션 작업 목표

1. **MultiNicNodeConfig CRD 정의 및 생성**
2. **Controller의 노드별 CR 감시 로직 구현**
3. **기본 Job 스케줄링 로직 수정**
4. **간단한 프로토타입으로 개념 검증**

---

**작성일**: 2025-08-29  
**세션**: 아키텍처 재설계 분석 완료  
**다음 단계**: MultiNicNodeConfig 기반 구현 시작