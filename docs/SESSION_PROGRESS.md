# MultiNIC Agent 점진적 개선 프로젝트

## 📅 세션 정보
- **시작일**: 2025-08-29
- **현재 상태**: 2단계 완료 - MultiNicNodeConfig CRD 생성 완료
- **다음 작업**: 3단계 Agent 데이터 소스 변경 (DB → Node CR)

## 🎯 프로젝트 목표

### 핵심 문제점 (기존 아키텍처)
1. **확장성 문제**: 클러스터 전체 CRD로 인한 etcd 용량 제한
2. **동시성 충돌**: 여러 Job이 동일한 CR 동시 업데이트
3. **보안 권한 과다**: Job Pod들이 클러스터 전체 수정 권한 필요
4. **아키텍처 안티패턴**: Job이 직접 CR 상태 업데이트

### 해결 방향
- **기존 방식**: ❌ 전체 아키텍처 재설계 (Clean Slate)
- **수정된 방식**: ✅ 검증된 로직 재사용 + 최소한 변경 + 안전한 Git 워크플로우

## 🔄 핵심 결정사항

### 1. Clean Slate 접근법 폐기
**이유**: 사용자 지적 - "DaemonSet vs Job = 단순히 실행 방식 차이"
- 네트워크 로직 자체는 검증되어 작동함
- 불필요한 복잡성과 리스크 제거
- 개발 효율성 우선

### 2. main 브랜치 기반 점진적 개선 채택
**장점**:
- 검증된 네트워크 로직 100% 재사용
- 최소한의 변경으로 최대 효과
- 각 단계별 독립 테스트 가능
- 언제든 되돌리기 가능한 안전성

### 3. Git 워크플로우 개선
```bash
git checkout main
git pull origin main
git checkout -b feature/node-based-clean-architecture
```

## 📋 7단계 구현 계획

### 0단계: Git 워크플로우 준비 ✅
**목표**: 안전한 개발 환경 구성
**상태**: ✅ 완료 (2025-08-29)
**작업**:
- ✅ main 브랜치로 체크아웃
- ✅ 새 브랜치 생성 (feature/node-based-clean-architecture)
- ✅ 깔끔한 시작점 확보

### 1단계: main 브랜치 분석 ✅
**목표**: 현재 네트워크 로직 파악 및 재사용 가능 부분 식별
**상태**: ✅ 완료 (2025-08-29)
**작업**:
- ✅ 네트워크 설정 로직 분석 (netplan/ifcfg)
- ✅ Agent 구조 파악
- ✅ 재사용 가능한 컴포넌트 식별
- ✅ 검증된 로직 100% 재사용 가능 확인

**🔑 주요 발견사항**:
- **Ubuntu Netplan 어댑터**: `internal/infrastructure/network/netplan_adapter.go` - 완전 재사용 가능
- **RHEL ifcfg 어댑터**: `internal/infrastructure/network/rhel_adapter.go` - 완전 재사용 가능  
- **OS 감지 팩토리**: `internal/infrastructure/network/factory.go` - 완전 재사용 가능
- **비즈니스 로직**: `internal/application/usecases/configure_network.go` - 90% 재사용 (데이터 소스만 변경)
- **Agent 프레임워크**: `cmd/agent/main.go` - 85% 재사용 (실행 모드만 변경)

### 2단계: MultiNicNodeConfig CRD 생성 📝
**목표**: 노드별 CRD 정의
**상태**: ⏳ 다음 세션 예정
**작업**: CRD 스키마 정의 및 생성

### 3단계: Agent 데이터 소스 변경 🔄
**목표**: DB 읽기 → NodeCR 읽기로 변경
**상태**: ✅ 완료 (2025-08-29)
**작업 결과**:
- ✅ 기존 네트워크 로직 100% 유지 (유스케이스와 네트워크 어댑터 무변경)
- ✅ 데이터 소스 DI 방식으로 교체 가능하도록 구현 (Clean Architecture 유지)
- ✅ `NodeCR` 기반 레포지토리 추가: `internal/infrastructure/persistence/nodecr_repository.go`
- ✅ 파일 기반 소스(테스트/로컬): `internal/infrastructure/persistence/nodecr_source_file.go`
- ✅ 컨테이너 DI 스위치: `DATA_SOURCE=nodecr` 시 DB 연결 없이 NodeCR 사용
- ✅ TDD 테스트 추가:
  - `internal/infrastructure/persistence/nodecr_repository_test.go`
  - `internal/infrastructure/container/container_nodecr_test.go`

**환경 변수 (구성 옵션)**:
- `DATA_SOURCE`: `db`(기본) | `nodecr`
- `NODE_CR_NAMESPACE`: NodeCR이 위치한 네임스페이스 (기본: `multinic-system`)

**구현 메모**:
- Kube API 기반 조회: `dynamic.Interface`로 `multinic.io/v1alpha1` `multinicnodeconfigs` 리소스 조회
- 테스트: client-go `dynamic/fake`로 가짜 클라이언트 사용 (실제 클러스터 불필요)

**주의**: NodeCR 아키텍처에서는 Agent가 CR `status`를 직접 수정하지 않음. `UpdateInterfaceStatus`는 no-op이며, 상태 업데이트는 5단계 Controller가 담당.

### 4단계: Agent 실행 방식 변경 ⚙️
**목표**: DaemonSet → Job 실행 방식 변경
**상태**: ⏳ 준비 중 (사전 작업 완료)
**사전 작업 결과**:
- ✅ 에이전트의 노드명 결정 로직 개선: `NODE_NAME`(Downward API의 `spec.nodeName`) > hostname 클린 순
- ✅ Kube API 기반 NodeCR 조회 구성 완료 (Dynamic Client)

**다음 작업**:
- Job 매니페스트/Helm 추가, `env: { name: NODE_NAME, valueFrom: { fieldRef: { fieldPath: spec.nodeName }}}`
- `nodeSelector`/`affinity` 기반 타겟팅

### 5단계: Controller 생성 🎛️
**목표**: CRD 감시 및 Job 스케줄링 로직 구현
**상태**: ⏳ 대기 중
**작업**:
- CRD Watch 로직
- Job 생성/관리 로직

### 6단계: 통합 테스트 및 검증 ✅
**목표**: 전체 플로우 검증
**상태**: ⏳ 대기 중
**작업**: E2E 테스트 및 성능 검증

## 💡 예상 효과

### 기술적 장점
- **확장성**: 노드당 ~5KB CR → 1000 노드도 문제없음
- **성능**: 병렬 처리, 부분 실패 격리
- **보안**: 최소 권한 원칙 준수
- **운영성**: 노드별 독립적 문제 해결

### 개발 효율성
- **리스크 최소화**: 검증된 코드 재사용
- **개발 시간**: 네트워크 로직 재작성 불필요
- **Git 히스토리**: 깔끔하고 추적 가능한 변경 이력
- **팀 협업**: 이해하기 쉬운 점진적 변화

## 🚀 다음 세션 가이드

### 시작 프롬프트
```
안녕하세요! 

docs/SESSION_PROGRESS.md를 확인하고 MultiNIC Agent 점진적 개선의 2단계를 시작해주세요.

현재 상태:
- ✅ 0단계: Git 워크플로우 준비 완료 (feature/node-based-clean-architecture 브랜치)
- ✅ 1단계: main 브랜치 네트워크 로직 분석 완료 (100% 재사용 가능 확인)

2단계 목표: MultiNicNodeConfig CRD 생성
- 노드별 CRD 스키마 정의 및 생성
- 기존 MultiNicBizConfig와 구별되는 새로운 구조
- spec.nodeName, spec.interfaces[] 구조로 단순화
- status 필드에 노드별 상태 관리

/implement MultiNicNodeConfig CRD --type crd --incremental --safe-workflow
```

### 진행 원칙
1. **단계별 완료**: 각 세션에서 하나의 명확한 단계만 완료
2. **컨텍스트 유지**: 매 세션 종료 시 이 문서 업데이트
3. **안전성 우선**: 언제든 되돌릴 수 있는 상태 유지
4. **검증 기반**: 각 단계별 독립 테스트 수행

---

**문서 최종 업데이트**: 2025-08-29 (1단계 완료)  
**다음 업데이트 예정**: 2단계 완료 후
