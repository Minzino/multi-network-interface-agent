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
- **날짜**: 2025-09-10  
- **진행 단계**: Clean Architecture 리팩터링 Phase 1 완료, Phase 2(성능/보안/운영성) 진행 중
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

### ✅ Phase 2 착수(성능/보안/운영성)
- 워커풀 메트릭 추가: `queue_depth`, `worker_active`, `worker_utilization`, `task_duration{status}`
- 워커풀 기능 강화: 옵션 패턴(이름/리트라이/패닉핸들러), 재시도 정책 훅, 패닉 복구, 지표 연동
- 오케스트레이터 경로에 워커풀 완전 연동(리트라이/타임아웃/최종 상태 집계)
- 보안 초안: 명령 실행 시 인젝션 위험 문자 검증 및 인자 마스킹, 파일 쓰기/삭제/디렉토리 생성 시 허용 경로 검증(`/etc/netplan`, `/etc/sysconfig/network-scripts`, `/etc/NetworkManager/system-connections`, `/var/lib/multinic/backups`)
 - Preflight Guard 도입: 적용 전 시스템 MAC 존재 확인(미존재 시 적용 없이 실패 처리 → 링크 플랩 방지)
 - 로그/메시지 영어화: Naming/오류 메시지 영어 일원화(Job summary reason 포함)
 - 재시도 정책 정교화: DomainError(Timeout/Network/System/Resource)만 재시도 대상으로 인정

### ✅ 대시보드/문서
- `docs/METRICS.md`: 지표 명세/버킷/알림 힌트/스크레이프 가이드 추가
- `docs/grafana/workerpool-dashboard.json`: 워커풀 관찰 대시보드 초안(큐/활성/사용률/지연 p50/90/99/리트라이/패닉)

### ✅ 테스트
- DriftDetector 단위 테스트 3케이스 추가 및 통과(Netplan/ifcfg/시스템 조합)
- usecases 통합 테스트 2건 스킵 해제 및 스텁 정밀화 완료(동시성 상한/리트라이 검증 통과)
- Preflight 옵션화 유닛 테스트 추가(인터페이스 UP 차단 동작 검증)
- 전체 빌드/테스트 통과

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
- 패키지 단위 검증: 전체 통과
  - `internal/application/usecases`: 워커풀 테스트(리트라이/패닉복구/큐깊이/동시성 상한) 통과
  - `internal/infrastructure/adapters`: 보안 테스트(명령 검증/인자 검증/경로 검증) 통과
  - `internal/infrastructure/network`/`persistence`/`controller`: 레거시 테스트 정비 후 통과
- 참고: 통합 테스트 스킵 해제 완료. 워커풀 동시성은 유닛/통합 모두에서 검증됨.
 - 카나리 관찰: 잘못된 MAC 포함 시 기존 빌드에서는 적용→검증→롤백 순서로 링크 플랩 가능성 확인. 최신 빌드(Preflight Guard) 배포 시 해당 항목은 적용 없이 즉시 실패 처리로 전환됨.

## 🔄 다음 단계
- [ ] 미사용 구 코드 제거 마무리 및 문서 주석 정리
- [ ] 워커풀 실제 운영 파라미터 튜닝(큐 크기, 백프레셔, 취소 전파 검증)
- [ ] 재시도/백오프 정책 실제 유스케이스 연계(네트워크 설정 실패/일시 오류 구분)
- [ ] 명령 실행 핫패스 프로파일링 및 배치/캐싱 전략 검토
- [ ] 통합 테스트 정비(도메인 엔티티 접근자 변경 반영) → 커버리지 80%+

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
- [ ] 워커풀 메트릭 스크레이핑 확인 및 대시보드 초안
- [ ] 보안 로깅/경로 검증 정책을 실제 어댑터 호출 경로에 단계적 적용

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
1. usecases 통합 테스트 스텁 정밀화 및 Skip 해제 → 커버리지 80%+
2. (옵션) Preflight에서 인터페이스 UP 차단을 설정 가능 옵션으로 추가(운영 정책 반영)
3. 재시도/백오프 정책을 에러타입/코드별로 세분화(운영 파라미터화)
4. CI 강화: `-race`/goleak 활성화
5. 대시보드 알람 룰 초안(PR 포함): 큐 깊이/실패율/리트라이/패닉 급증
6. AgentInfo에 버전 라벨 노출 및 컨트롤러 시작 로그에 커밋/버전 출력(운영 추적성)

## 🔎 운영 메모(OpenStack)
- MTU 1450(오버레이) 여부 확인 후 CR의 MTU 값 조정 권장
- 테스트 NIC는 운영 트래픽 미사용/NIC DOWN 상태에서 시작 시 검증 명확
- 최신 이미지(Preflight Guard 포함)로 카나리 2노드×NIC 4개 재검증 권장

---

## 🧭 OS별 네트워크 적용/영속화 전략 (결정)

### 공통 원칙 (안정성 우선)
- 런타임 적용은 `ip` 계열로 즉시 반영: 이름(rename), MTU, IPv4, 라우트 모두 `ip`/`ip route`로 처리
  - 예) `ip link set dev <old> name <new>`, `ip link set <new> mtu <mtu>`, `ip addr replace <ip>/<prefix> dev <new>`, `ip route replace ...`
- 영속성은 OS별 파일 “작성만” 수행, 즉시 apply/reload 호출은 하지 않음(유지보수 창 배치 1회 적용 옵션만)
- Preflight: MAC 미존재/오류 즉시 차단 + 인터페이스 UP 차단 기본 활성
- 동시성: 기본 `MAX_CONCURRENT_TASKS=1` 권장, 라우트/기본경로 변경은 전역 직렬화

### Ubuntu/Debian (netplan)
- netplan YAML에 이름 영속화를 위한 `set-name` 반드시 포함 (이전에는 누락되어 부팅 시 ensX로 복귀)
- 표준 템플릿 예시:

```yaml
network:
  version: 2
  renderer: networkd   # 또는 NetworkManager 환경에 맞게
  ethernets:
    multinic0:
      match:
        macaddress: fa:16:3e:11:4c:d1
      set-name: multinic0      # 이름 영속화(핵심)
      mtu: 1450
      dhcp4: false
      addresses:
        - 11.11.11.107/24
      optional: true
```

- 운영: 런타임은 `ip`로 즉시 반영, YAML은 영속만(즉시 `netplan apply` 비호출). 유지보수 창에 1회 apply/재부팅으로 반영 확인

### RHEL/Rocky/Alma/SUSE
- 이름 영속: systemd-udev `.link` 파일로 MAC→Name 고정
- 설정 영속: NetworkManager keyfile(`.nmconnection`, 권한 600) 또는 SUSE ifcfg(wicked)
- 예시(.link): `/etc/systemd/network/10-multinic0.link`

```
[Match]
MACAddress=fa:16:3e:11:4c:d1
[Link]
Name=multinic0
```

- 예시(.nmconnection): `/etc/NetworkManager/system-connections/multinic0.nmconnection` (600, root:root, SELinux 시 `restorecon`)

```
[connection]
id=multinic0
type=ethernet
interface-name=multinic0
autoconnect=true

[ethernet]
mac-address=fa:16:3e:11:4c:d1
mtu=1450

[ipv4]
method=manual
address1=11.11.11.107/24
never-default=true

[ipv6]
method=ignore
```

- 운영: 런타임은 `ip`로 즉시 반영, `.link`/`.nmconnection`은 영속만. 즉시 `nmcli reload/up` 비호출(배치 옵션 시 1회)
- 디렉터리 준비: `/etc/systemd/network`가 없을 수 있어 자동 생성 필요 (코드에 허용/생성 반영 완료)

### 검증/운영 팁
- 재부팅 후 이름/설정이 유지되는지 확인: `ip -o link/addr show multinicX`, `nmcli device status`
- 기본 경로를 만들지 않으려면 `never-default=true` 유지. 게이트웨이 영속이 필요하면 `address1=IP/prefix,GW` 또는 `route1=default 0.0.0.0/0 GW`를 사용(유지보수 창 권장)

### 구현 계획 (다음 단계)
- Ubuntu netplan 작성 로직에 `set-name` 추가(이름 영속)
- RHEL 경로에서 `.link` + `.nmconnection(600)` 생성, SELinux 컨텍스트 복원
- 런타임 경로 공통화(ip-only), 즉시 apply/reload 제거(배치 옵션만)
- 라우트 변경 전역 직렬화 + 기본 동시성 1
- 문서/메트릭/알람 갱신(preflight_up_block, mac_missing_block, runtime_apply_duration 등)

---

## ✅ 현재까지 반영 사항(상세)

### Ubuntu/Debian
- 런타임: netplan 즉시 적용(try/apply) 제거, `ip` 기반으로 rename/MTU/IP/UP 적용
- 영속: netplan YAML에 `match.macaddress + set-name` 항상 포함(이름 영속화 보장), 파일만 작성(유지보수 창 1회 apply 또는 재부팅 시 반영)
- TDD: netplan 경로 단위 테스트 추가
  - set-name 포함 확인, netplan try/apply 미호출 확인, 롤백은 파일 삭제만 확인
- 파일 네이밍: `90-` 접두 사용 유지 (예: `90-multinic0.yaml`)

### RHEL/Rocky/Alma/SUSE
- 런타임: `ip` 기반으로 rename/MTU/IP/UP 적용(즉시 `nmcli`/`systemctl restart NetworkManager` 제거)
- 영속: systemd-udev `.link` + NetworkManager `.nmconnection` “작성만” 수행
  - `.link`: `/etc/systemd/network/90-multinicX.link`
  - `.nmconnection`: `/etc/NetworkManager/system-connections/90-multinicX.nmconnection` (권한 600, SELinux 시 `restorecon` 권장)
  - 기본 경로 방지: `never-default=true` 기본 포함(게이트웨이는 유지보수 창에 영속 선언)
- Helm: `/etc/systemd/network` hostPath 마운트 추가, DB 환경변수 제거(DATA_SOURCE=nodecr)
- TDD: RHEL 경로 단위 테스트 추가
  - persist 파일 생성/내용 확인, 즉시 systemctl/nmcli 호출 없음 확인, `90-` 접두 확인

### Preflight/UseCase/워크플로우
- Preflight: MAC 미존재/오류 즉시 차단 + 인터페이스 UP 차단(기본) 반영
- 워커풀: after-hook 결과 집계/리트라이/메트릭 유지, 통합 테스트 정비(동시성/리트라이)
- 로그/문서: 영어화, METRICS.md/대시보드, 운영 메모 정리

### Helm/배포 스크립트
- Helm
  - 불필요한 DB env 제거, `agent.dataSource=nodecr` 기본값 추가
  - `/etc/netplan`, `/etc/NetworkManager/system-connections`, `/etc/systemd/network` 마운트
- deploy.sh
  - 사용자 요청에 따라 nerdctl-only 원복(기존 잘 동작하던 버전 유지)
  - buildkitd는 외부에서 기동되어 있어야 함(재부팅 후 필요 시 수동 기동)

---

## 🧪 테스트/TDD 현황
- Ubuntu netplan: runtime-ip + persist-only 테스트 통과
- RHEL persist: `.link` + `.nmconnection` 생성/내용/미호출 경로 테스트 통과
- UseCases: 프리플라이트/통합테스트 정비 및 통과(동시성/리트라이)

---

## 🛠 운영 체크리스트(현장)
- Ubuntu
  - YAML에 `set-name` 포함되어야 재부팅 후 ensX로 돌아가지 않음
  - 런타임은 `ip`, YAML은 영속만(유지보수 창 1회 apply)
- RHEL
  - `.link`(644) + `.nmconnection`(600) 작성, 경로 없으면 생성됨
  - SELinux enforcing 시 `restorecon -Rv /etc/NetworkManager/system-connections`
  - 런타임은 `ip`, 즉시 nmcli/systemctl 호출 없음(플랩 최소화)
- 공통
  - 기본 경로/게이트웨이는 유지보수 창에 배치 1회로 반영 권장
  - 동시성 1 권장, 라우트 변경 직렬화

---

## ✅ Phase A 완료: 라우팅/기본경로 전역 직렬화 + 기본 동시성 1

### 2025-09-11 완료사항
1. **SELinux 컨텍스트 복원 기능 추가**:
   - RHELAdapter에 `enableSELinuxRestore` 옵션 추가 (기본 OFF)
   - `NewRHELAdapterWithSELinux` 생성자 및 `restoreSELinuxContext` 메서드 구현
   - SELinux enforcing 환경에서 .nmconnection 파일 생성 후 `restorecon -Rv` 실행
   - 컨테이너 환경에서는 nsenter 사용하여 호스트 네임스페이스에서 실행
   - 포괄적 단위 테스트 작성 및 통과

2. **라우팅 전역 직렬화 인프라 구축**:
   - `RoutingCoordinator` 서비스 생성 (`/internal/domain/services/routing_coordinator.go`)
   - 전역 뮤텍스 기반 라우팅 작업 직렬화 구현
   - Prometheus 메트릭 수집 (작업 지속시간, 성공/실패율)
   - 컨텍스트 취소 및 타임아웃 지원

3. **기본 동시성 설정 변경**:
   - `DefaultMaxConcurrentTasks`: 5 → 1로 변경
   - Helm values.yaml에 `maxConcurrentTasks: 1` 기본값 설정
   - 라우팅 테이블 충돌 방지하면서 설정 가능성 유지

4. **UseCase 통합**:
   - ConfigureNetworkUseCase, DeleteNetworkUseCase에 RoutingCoordinator 연결
   - 모든 라우팅 작업이 전역 직렬화를 통해 처리되도록 구현
   - 기존 생성자 호환성 유지 (자동 RoutingCoordinator 생성)

5. **테스트 완전성 확보**:
   - 라우팅 코디네이터 단위 테스트 (동시성, 메트릭, 컨텍스트 취소)
   - 통합 테스트 및 UseCase 테스트 수정 및 통과 확인
   - 전체 프로젝트 테스트 통과 (✅ 100% 성공)

### 핵심 성과
- **라우팅 충돌 방지**: 전역 직렬화로 동시 인터페이스 설정 시 라우팅 테이블 충돌 완전 차단
- **성능 균형**: 기본 동시성 1로 안전성 확보, 필요시 설정 변경으로 성능 조정 가능
- **운영 관찰성**: 라우팅 작업 메트릭으로 성능 및 락 경합 모니터링 가능
- **생산 준비**: 에러 처리, 컨텍스트 취소, 타임아웃 모든 고려사항 반영

## 🔜 다음 단계(실행 계획)
1) ✅ RHEL SELinux 컨텍스트 복원 로직(옵션) 추가 및 TDD - **완료**
2) ✅ 라우트/기본경로 변경 전역 직렬화 + 기본 동시성 1(Helm values) 반영 - **완료**
3) 통합 테스트: 재부팅 영속 시나리오(간접 검증: persist 파일 기준) 케이스 추가
4) 문서/알람/메트릭 라벨 최종 정리(runtime_apply_duration, route_changes 등)

---

## 🪄 매직 프롬프트 (다음 세션)

```
당신은 Go 기반 MultiNIC Agent 리팩터링을 이어받는 시니어 엔지니어입니다.

[목표]
- RHEL/SUSE 경로를 Ubuntu와 동일 철학으로 완성: 런타임 ip-only, 영속 파일(.link/.nmconnection) “작성만”, 즉시 apply/reload 없음
- SELinux 컨텍스트 복원(옵션) 추가, 라우트 변경 직렬화, 기본 동시성 1(Helm values)
- TDD로 보장(파일 생성/권한/내용/미호출 경로 검증)

[현황 요약]
- Ubuntu: netplan set-name 포함, netplan try/apply 제거, ip-only 런타임(TDD 완료)
- RHEL: .link + .nmconnection(90- 접두) 생성 및 ip-only 런타임(TDD 완료), Helm에 systemd-network 마운트 추가
- Helm: DB env 제거(DATA_SOURCE=nodecr), deploy.sh는 nerdctl-only로 원복
- Preflight: MAC 미존재/오류/UP 차단(기본)

[할 일]
1) RHEL: SELinux `restorecon` 호출(옵션) 추가 및 테스트 스텁 반영
2) 라우트/기본경로 변경 전역 직렬화(전역 락/세마포) 및 기본 동시성 1(Helm values)
3) 재부팅 영속 시나리오 통합 테스트(간접: persist 파일 기준) 보강
4) 문서/알람/메트릭 라벨 보강

[주의]
- .nmconnection 권한 600 유지, .link 644
- 게이트웨이/기본 경로는 유지보수 창 배치 1회로 처리
- Ubuntu는 netplan set-name으로 이름 영속, RHEL은 .link로 영속
```


---

## 🪄 매직 프롬프트 (다음 세션용)

```
당신은 Go 기반 MultiNIC Agent 리팩터링을 이어받는 시니어 엔지니어입니다. 아래 결정사항을 기준으로 구현을 진행하세요.

[목표]
- 안정성 최우선: 런타임은 ip 기반 즉시 적용, 영속은 OS별 파일만 작성(즉시 apply/reload 없음)
- Ubuntu/Debian: netplan YAML에 match.macaddress + set-name 필수(이름 영속)
- RHEL/SUSE: systemd .link로 이름 영속 + .nmconnection(600)로 설정 영속
- Preflight: MAC 미존재/오류/UP 차단(기본) 유지
- 동시성 1 + 라우트 변경 직렬화

[해야 할 일]
1) Ubuntu 경로: netplan 템플릿에 set-name 추가, 현재 런타임 ip 적용 로직과 정합성 유지
2) RHEL 경로: .link + .nmconnection 생성(권한/SELinux 포함), `/etc/systemd/network` 자동 생성 보장
3) 런타임 처리 공통화: rename/mtu/ip/route를 ip-only로 일원화, 즉시 nmcli/netplan/wicked 적용 제거(배치 옵션만)
4) 라우트/기본경로 변경 직렬화, 기본 동시성 1로 설정(Helm values)
5) 메트릭/알람 라벨 보강 및 문서 업데이트
6) 통합/재부팅 테스트 시나리오 추가(이름/설정 영속성 검증)

[참고]
- RHEL: .link(644) + .nmconnection(600), SELinux `restorecon -Rv /etc/NetworkManager/system-connections`
- Ubuntu: netplan set-name로 rename 영속, optional: true 권장
- 런타임은 항상 ip replace 계열 사용, 기본 경로 변경은 유지보수 창에 배치 1회로 처리
```
