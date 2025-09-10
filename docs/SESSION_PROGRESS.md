# MultiNIC Agent TDD 리팩터링 프로젝트

## 🌐 프로젝트 개요
**MultiNIC Agent**는 Kubernetes 클러스터 노드에서 여러 네트워크 인터페이스를 자동으로 구성하고 관리하는 Go 언어 기반 에이전트입니다.

### 핵심 기능
- **다중 네트워크 인터페이스 설정**: multinic0, multinic1, multinic2 등 여러 가상 인터페이스 생성 및 설정
- **OS별 네트워크 관리**: Ubuntu (Netplan), RHEL/CentOS (NetworkManager/ifcfg) 지원
- **동적 네트워크 구성**: 실시간 IP 주소, MTU, CIDR 설정 및 변경 감지
- **고아 인터페이스 정리**: 더 이상 사용되지 않는 네트워크 설정 자동 정리
- **안전한 롤백**: 네트워크 설정 실패 시 이전 상태로 자동 복구

## 📅 현재 상태
- **날짜**: 2025-09-09  
- **진행 단계**: Clean Architecture 리팩터링 Phase 1 완료, Phase 2 준비 착수
- **목표**: 80%+ 커버리지, 성능 최적화(워커풀, 타임아웃 일원화)

## 🎯 완료된 작업 (최신)

### ✅ Phase 1: 아키텍처 리팩터링 (1차/2차)
- 1차: 거대 UseCase 내부 헬퍼 로직 분리 (파일/드리프트/시스템체크/판단) → 안전한 파일 분리
- 2차: DriftDetector 도메인 서비스 도입 및 DI 연계
  - Netplan/ifcfg 파싱 및 드리프트 검출 로직 서비스화
  - 시스템 상태 검증(MAC 존재/UP) 포함, 메트릭 유지
  - 컨테이너 DI 추가, UseCase에서 서비스 호출하도록 변경
- 인프라/퍼시스턴스 정합성 수정
  - RHEL/Netplan 어댑터: 엔티티 접근자 메서드 호출로 교정
  - MySQL/NodeCR Repository: `NewNetworkInterface` 사용으로 일관성 확보

### ✅ UseCase 구조 정돈
- Orchestrator 도입: `ApplyUseCase`/`ValidateUseCase`/`ProcessingUseCase` 구성
- `processInterface` → Processor 위임, 상태 업데이트/메트릭 일원화

### ✅ 성능 준비
- 워커풀 스켈레톤(`WorkerPool[T]`) 추가: 큐 기반 작업 처리 준비
- Execute 경로 워커풀 전환 및 per-interface timeout 적용
- 타임아웃 일원화: `Agent.CommandTimeout` 사용 (없으면 기본 30s)

### ✅ 테스트
- DriftDetector 단위 테스트 3케이스 추가 및 통과(Netplan/ifcfg/시스템 조합)
- 전체 빌드 성공

## 🎯 현재 달성 상황

### ✅ TDD 테스트 구현 완료
1. **ConfigureNetwork 유스케이스**: 
   - 7개 테스트 케이스 완료 (Execute: 6개, processInterface: 1개)
   - 성공/실패/롤백/검증/드리프트 감지 등 포괄적 테스트

2. **DeleteNetwork 유스케이스**:
   - 8개 테스트 케이스 완료 (Execute: 6개, OrphanDetection: 3개)
   - Ubuntu/RHEL OS별 고아 인터페이스 정리 테스트
   - 전체 정리 모드 및 에러 처리 테스트
   - 고아 인터페이스 감지 알고리즘 테스트

## 📊 현재 테스트 현황
- **전체 테스트**: ✅ 15개 케이스 모두 통과
- **현재 커버리지**: 54.7%
- **목표 커버리지**: 90%
- **테스트 상태**: 모든 유스케이스 TDD 구현 완료

## 🔄 다음 단계
- [ ] 미사용 구 코드 제거 마무리 및 문서 주석 정리
- [ ] 워커풀 실제 운영 파라미터 튜닝(큐 크기, 백프레셔, 취소 전파 검증)
- [ ] Phase 2: 명령 실행 최적화(배치/리트라이/백오프), 캐싱 도입
- [ ] Phase 3: 민감정보 마스킹/경로 검증/명령 인젝션 방어 강화
- [ ] 커버리지 80%+ 달성 위한 단위/통합 테스트 보강

---

## ▶️ 다음 세션 준비 (핵심 요약)

- 브랜치: `refactor/phase1-configure-usecase-extract`
- 현재 상태: Phase 1(아키텍처 정리) 완료, Phase 2(성능/보안/운영성) 준비 완료
- 주요 변경 요점
  - DriftDetector 서비스 도입(파일/시스템 상태 기반 드리프트 판정)
  - Orchestrator(Apply/Validate/Processing) 분리로 가독성/테스트성↑
  - 워커풀 기반 동시 처리 전환 + per-interface 타임아웃 일원화
  - 어댑터/레포지토리 정합성(엔티티 접근자/생성자) 정리
- 중요한 운영 파라미터
  - `MAX_CONCURRENT_TASKS`: 동시에 처리할 인터페이스 수(초기 권장 2~5)
  - `COMMAND_TIMEOUT`: 인터페이스 1건 처리 타임아웃(기본 30s)

### 다음 세션 Kickoff 체크리스트
- [ ] `go build ./...`로 빌드 확인
- [ ] 서비스 테스트: `go test ./internal/domain/services -run DriftDetector -v`
- [ ] 운영 파라미터 시범값 설정(`MAX_CONCURRENT_TASKS`, `COMMAND_TIMEOUT`)
- [ ] 메트릭/로그에 워커풀 관측 지표 설계(큐 깊이, 워커 사용률, 처리 지연)
- [ ] 보안 로깅 정책(민감정보 마스킹) 초안 확정

### 다음 세션 Backlog(우선순위 제안)
1) 성능/운영성
   - 워커풀 메트릭 추가(큐 길이, 워커 사용률, 처리 지연 히스토그램)
   - 재시도/재등록 정책 훅, 패닉 복구
   - 파일/명령 호출 배치·캐싱 전략 검토
2) 보안 강화
   - 로그 마스킹(MAC/IP), 파일 경로 검증, 명령 인젝션 방어
3) 테스트 강화
   - Orchestrator/워커풀 경로 유닛/통합 테스트 보강, 커버리지 80%+

---

## 🪄 매직 프롬프트 (다음 세션 복사용)

다음 프롬프트를 복사해 새 세션 시작 시 붙여넣으면, 본 레포의 컨텍스트와 작업 원칙을 빠르게 주입할 수 있습니다.

```
당신은 Go 기반 MultiNIC Agent 리팩터링을 이어받는 시니어 엔지니어입니다. 다음 원칙을 반드시 지키십시오.

목표와 컨텍스트
- 레포: multinic-agent (Go, Clean Architecture)
- 상태: Phase 1(아키텍처 정리) 완료, Phase 2(성능/보안/운영성) 수행
- 브랜치: refactor/phase1-configure-usecase-extract
- 핵심 구조: ConfigureNetworkUseCase(오케스트레이터) + DriftDetector + WorkerPool
- 운영 파라미터: MAX_CONCURRENT_TASKS(동시 처리 수), COMMAND_TIMEOUT(인터페이스별 타임아웃)

작업 원칙
1) 작은 커밋, 한글 커밋 메시지(Conventional Commits)로 의미 단위 기록
2) SESSION_PROGRESS.md에 진행사항/체크리스트 반드시 반영
3) 동작 변경 시 반드시 테스트(가능하면 단위→통합 순)
4) 성능/보안/운영성 영향 항목은 근거와 함께 설명
5) 기존 스타일/접근자/생성자 규약 준수(엔티티는 New* + 게터 사용)
6) DI/컨테이너 변경 시 영향 범위를 명확히 기술

이번 세션 우선순위(제안)
- 워커풀 메트릭(큐 깊이, 워커 사용률, 처리 지연) 추가
- 재시도/재등록 정책 훅 및 패닉 복구
- 로그 마스킹·경로 검증·명령 인젝션 방어 초안
- 테스트 강화로 커버리지 80%+ 접근

출력 형식
- 먼저 계획(작은 단계) → 변경(코드/패치) → 검증(빌드/테스트) → 문서 반영(SESSION_PROGRESS.md)
- 필요시 선택지 제시 및 명확한 권고안 포함
```


## 🔧 기술 스택
- **언어**: Go 1.24.4
- **테스팅**: testify/mock, testify/assert
- **아키텍처**: Clean Architecture (4계층)
- **방법론**: TDD (Test-Driven Development)

## 📝 다음 단계
1. DeleteNetwork 유스케이스 TDD 테스트 완성
2. 전체 테스트 커버리지 90% 달성
3. 통합 테스트 및 성능 최적화
4. 문서화 및 배포 준비
