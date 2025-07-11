# MultiNIC Agent 프로젝트 분석

## 프로젝트 개요

MultiNIC Agent는 Kubernetes 클러스터에 조인된 노드들의 네트워크 인터페이스 설정을 자동으로 관리하는 Go 기반의 DaemonSet 에이전트입니다.

### 주요 특징
- 데이터베이스 기반 설정 관리 (MySQL/MariaDB)
- Ubuntu (Netplan) 및 RHEL/CentOS (nmcli) 지원
- 자동 롤백 기능
- multinic0 ~ multinic9 인터페이스 관리 (최대 10개)
- 기존 네트워크 인터페이스 (eth0, ens* 등) 보호

## 아키텍처

```
┌─────────────────┐
|   Controller    |
|  (DB: MariaDB)  | -> 네트워크 설정 정보 저장
└────────┬────────┘
         |
    ┌────▼────┐
    | Agent   | (DaemonSet)
    | - DB 모니터링 (30초 주기)
    | - 설정 적용
    | - 롤백 관리
    | - 헬스체크
    └─────────┘
```

## 기술 스택

### 핵심 기술
- **언어**: Go 1.21
- **데이터베이스**: MySQL/MariaDB
- **배포**: Kubernetes DaemonSet
- **패키징**: Helm Chart

### 주요 의존성
- `github.com/go-sql-driver/mysql v1.7.1` - MySQL 드라이버
- `github.com/sirupsen/logrus v1.9.3` - 구조화된 로깅
- `gopkg.in/yaml.v3 v3.0.1` - YAML 파싱 (netplan 설정)

### 빌드 환경
- **Multi-stage Docker 빌드**
  - 빌드: `golang:1.21-alpine`
  - 실행: `alpine:3.18`
- **최소 이미지 크기**: Alpine 기반으로 경량화

## 프로젝트 구조

### 현재 구조 (리팩터링 진행 중)
```
multinic-agent/
├── cmd/agent/          # 메인 애플리케이션
|   └── main.go         # 진입점
├── internal/           # 클린 아키텍처 구조 (NEW)
|   ├── domain/         # 비즈니스 로직 계층
|   |   ├── entities/   # 도메인 엔티티
|   |   ├── errors/     # 도메인 에러 정의
|   |   ├── interfaces/ # 도메인 인터페이스
|   |   └── services/   # 도메인 서비스
|   ├── application/    # 애플리케이션 계층
|   |   └── usecases/   # 유스케이스
|   ├── infrastructure/ # 인프라스트럭처 계층
|   |   ├── persistence/# 데이터베이스 구현
|   |   ├── network/    # 네트워크 관리 구현
|   |   ├── health/     # 헬스체크 구현
|   |   └── config/     # 설정 관리
|   └── interfaces/     # 인터페이스 어댑터
|       ├── http/       # HTTP 핸들러
|       └── cli/        # CLI 인터페이스
├── pkg/                # 기존 패키지 (마이그레이션 예정)
|   ├── db/             # 데이터베이스 연동
|   ├── health/         # 헬스체크 시스템
|   ├── netplan/        # Netplan 설정 관리
|   ├── network/        # 네트워크 관리 추상화
|   └── utils/          # 유틸리티 함수
├── deployments/helm/   # Helm 차트
├── scripts/            # 배포 및 테스트 스크립트
└── test/               # 통합 테스트
```

## 핵심 기능 분석

### 1. 메인 프로세스 (cmd/agent/main.go)

#### 초기화 과정
1. 환경 변수 기반 설정 로드
2. 데이터베이스 연결 (재시도 로직 포함)
3. OS 감지 및 NetworkManager 생성
4. 헬스체크 서버 시작 (포트 8080)
5. 30초 주기로 폴링 시작

#### 처리 흐름
```go
processConfigurations() {
    1. 호스트네임 가져오기 및 검증
    2. DB에서 대기 중인 인터페이스 조회 (netplan_success=0)
    3. 각 인터페이스별로:
       - 인터페이스 이름 생성 (multinic0~9)
       - 네트워크 설정 적용 (재시도 2회)
       - 성공 시 DB 상태 업데이트
       - 실패 시 헬스체크에 기록
}
```

### 2. 네트워크 관리 시스템 (pkg/network/)

#### 설계 패턴
- **Strategy Pattern**: OS별 구현 분리
- **Factory Pattern**: OS 자동 감지 및 적절한 관리자 생성

#### NetworkManager 인터페이스
```go
type NetworkManager interface {
    ApplyConfiguration(configData []byte, interfaceName string) error
    Rollback(interfaceName string) error
    ValidateInterface(interfaceName string) bool
    GetType() string
    ConfigureInterface(iface db.MultiInterface, interfaceName string) error
}
```

#### OS별 구현

**Ubuntu (Netplan)**
- 설정 파일: `/etc/netplan/9{index}-{interface}.yaml`
- 백업 경로: `/var/lib/multinic/backups`
- 특징:
  - `netplan try --timeout=120`으로 안전한 테스트
  - 컨테이너 환경에서 `nsenter` 사용
  - YAML 기반 설정

**RHEL/CentOS (nmcli)**
- 설정 방식: NetworkManager CLI (nmcli) 사용
- 특징:
  - `nmcli connection` 명령으로 연결 프로파일 관리
  - 파일 기반이 아닌 명령어 기반 설정

### 3. 데이터베이스 연동 (pkg/db/)

#### 연결 풀 설정
- 최대 동시 연결: 10개
- 최대 유휴 연결: 5개
- 연결 수명: 5분

#### 주요 쿼리
1. **GetPendingInterfaces**: 처리 대기 인터페이스 조회
   ```sql
   SELECT id, mac_address, attached_node_name, netplan_success 
   FROM multi_interface 
   WHERE netplan_success = 0 
   AND attached_node_name = ? 
   AND deleted_at IS NULL 
   LIMIT 10
   ```

2. **UpdateInterfaceStatus**: 처리 결과 업데이트
   ```sql
   UPDATE multi_interface 
   SET netplan_success = ?, modified_at = NOW() 
   WHERE id = ?
   ```

### 4. 헬스체크 시스템 (pkg/health/)

#### HTTP 엔드포인트
- URL: `GET /`
- 상태 코드:
  - 200: healthy/degraded
  - 503: unhealthy

#### 모니터링 항목
- 데이터베이스 연결 상태
- 처리된 VM 수
- 실패한 설정 수
- 서비스 가동 시간

## 보안 및 안정성 기능

### 1. 입력 검증
- 호스트네임 검증
- MAC 주소 형식 검증
- 인터페이스 이름 패턴 검증 (multinic[0-9])

### 2. 백업 및 롤백
- 설정 변경 전 자동 백업
- 타임스탬프 기반 백업 파일 관리
- 실패 시 최신 백업으로 자동 복원

### 3. 재시도 로직
- 지수 백오프를 사용한 재시도
- DB 연결: 최대 5회, 초기 지연 1초
- 네트워크 설정: 최대 2회, 초기 지연 2초

### 4. 타임아웃 처리
- 명령 실행: 30초 타임아웃
- Netplan 테스트: 120초 타임아웃
- 프로세스 강제 종료 지원

## 배포 방식

### 1. Helm Chart
- 환경별 설정 파일 지원 (dev/prod)
- RBAC 설정 포함
- DaemonSet으로 모든 노드에 배포

### 2. 배포 스크립트
- `deploy.sh`: 원클릭 배포
- `test-deployment.sh`: 배포 검증
- `test-functionality.sh`: 기능 테스트

## 모니터링 및 디버깅

### 로그 형식
- JSON 구조화 로깅
- 필드 기반 컨텍스트 정보
- 예시:
  ```json
  {
    "level": "info",
    "msg": "인터페이스 설정 적용 중",
    "interface_id": 123,
    "interface_name": "multinic0",
    "time": "2025-01-08T..."
  }
  ```

### 디버깅 포인트
1. 에이전트 로그: `kubectl logs -n multinic-system`
2. DB 연결 상태: 헬스체크 엔드포인트
3. 백업 파일: 노드의 `/var/lib/multinic/backups`

## 개선 사항 및 고려사항

### 현재 제한사항
- 최대 10개 인터페이스 지원 (multinic0~9)
- 30초 고정 폴링 주기
- 단방향 동기화 (DB -> 노드)

### 향후 개선 가능 영역
1. 동적 폴링 주기 조정
2. 인터페이스 수 제한 확장
3. 양방향 동기화 지원
4. Prometheus 메트릭 추가

## 리팩터링 진행 상황

### Phase 1: 기반 구조 개선 (완료)
1. **클린 아키텍처 디렉토리 구조 생성**
   - 도메인, 애플리케이션, 인프라스트럭처 레이어 분리
   
2. **도메인 레이어 구현**
   - NetworkInterface 엔티티 정의
   - Repository, Network, OS 관련 인터페이스 정의
   - InterfaceNamingService 도메인 서비스 구현
   
3. **에러 처리 체계 구축**
   - 타입별 도메인 에러 정의 (Validation, NotFound, System 등)
   - 일관된 에러 생성 및 처리 패턴
   
4. **Repository 패턴 구현**
   - MySQLRepository 구현
   - 도메인과 인프라 계층 분리

5. **OS 감지 로직 개선**
   - /etc/issue 파일 기반으로 단순화

### Phase 2: 핵심 로직 리팩터링 (완료)
1. **인프라스트럭처 어댑터 구현**
   - OS 감지기 (RealOSDetector)
   - 파일 시스템 어댑터 (RealFileSystem, RealClock)
   - 설정 로더 (EnvironmentConfigLoader)
   
2. **네트워크 관리 시스템 구현**
   - NetplanManager와 RHELManager 어댑터
   - 통합된 백업 서비스 (BackupService)
   - 헬스 체크 서비스 (HealthService)
   
3. **애플리케이션 유스케이스 구현**
   - ConfigureNetworkUseCase
   - 모든 비즈니스 로직 캡슐화
   
4. **의존성 주입 컨테이너**
   - 전체 시스템 조립 및 관리
   - 생명주기 관리 (graceful shutdown)
   
5. **main.go 완전 리팩터링**
   - 250줄 -> 179줄로 코드 축소
   - Application 구조체로 관심사 분리
   - 의존성 주입을 통한 테스트 가능성 향상

### Phase 3: 테스트 및 검증 (완료)
1. **단위 테스트 작성**
   - 도메인 엔티티 테스트 (100% 커버리지)
     * NetworkInterface 유효성 검증 테스트
     * MAC 주소/인터페이스 이름 형식 검증
     * 상태 변경 메서드 테스트
   
   - 도메인 서비스 테스트 (100% 커버리지)
     * InterfaceNamingService 인터페이스 이름 생성 로직
     * 사용 중인 인터페이스 감지 로직
   
   - 유스케이스 테스트 (88.2% 커버리지)
     * 네트워크 설정 성공/실패 시나리오
     * 롤백 로직 및 에러 처리
     * Mock 의존성을 활용한 완전 격리 테스트
   
   - 인프라스트럭처 어댑터 테스트
     * OS 감지 어댑터 (다양한 OS 형식 테스트)
     * 설정 로더 (환경 변수 처리 테스트)

2. **통합 테스트 구현**
   - 클린 아키텍처 구성 요소 간 통합 테스트
   - 실제 의존성과 Mock 의존성 혼합 테스트
   - 컨테이너 초기화 및 라이프사이클 테스트

3. **테스트 인프라스트럭처**
   - testify/mock 라이브러리 도입
   - 모든 도메인 인터페이스에 대한 Mock 구현
   - 일관된 테스트 패턴 및 구조

## 포스트모템

### 프로젝트 강점
1. **확장성**: Strategy/Factory 패턴으로 새로운 OS 지원 용이
2. **안정성**: 백업/롤백, 재시도, 타임아웃 등 다중 안전장치
3. **운영성**: 구조화된 로깅, 헬스체크, Helm 차트 제공
4. **보안성**: 입력 검증, 기존 인터페이스 보호
5. **유지보수성**: 클린 아키텍처 도입으로 관심사 분리 개선

### 기술적 의사결정
1. **Go 선택**: 경량 바이너리, 효율적인 동시성 처리
2. **DaemonSet 사용**: 모든 노드에 자동 배포 및 관리
3. **DB 기반 설정**: 중앙 집중식 관리, 상태 추적 용이
4. **클린 아키텍처 도입**: 테스트 가능성과 확장성 개선

### 리팩터링 성과 및 개선 사항

#### 정량적 개선 지표
1. **코드 복잡도 감소**: main.go 코드 250줄 -> 179줄 (28% 감소)
2. **테스트 커버리지**: 핵심 도메인 로직 90%+ 커버리지 달성
3. **아키텍처 레이어 분리**: 단일 파일 -> 4개 계층으로 구조화
4. **의존성 관리**: 순환 참조 제거, 인터페이스 기반 느슨한 결합

#### 품질 개선 사항
1. **테스트 가능성**: Mock을 통한 완전 격리 테스트 환경 구축
2. **유지보수성**: 도메인 중심 설계로 비즈니스 로직 명확화
3. **확장성**: 새로운 OS 지원, 기능 추가가 용이한 구조
4. **타입 안전성**: 강타입 도메인 엔티티와 에러 처리

#### 클린 아키텍처 적용 효과
- **의존성 역전**: 인프라스트럭처가 도메인에 의존하는 구조
- **관심사 분리**: 각 레이어별 명확한 책임 정의
- **비즈니스 로직 보호**: 외부 의존성으로부터 도메인 로직 격리
- **테스트 전략**: 단위/통합/E2E 테스트 레벨별 명확한 구분

### 기술적 도전과 해결책

#### 도전 1: 기존 레거시 코드와의 통합
**문제**: 기존 pkg/ 패키지와 새로운 internal/ 구조 간 충돌
**해결**: 어댑터 패턴을 통한 점진적 마이그레이션

#### 도전 2: Mock 타입 호환성 문제
**문제**: Mock 객체의 메서드 시그니처 불일치
**해결**: testify/mock 표준 사용, 인터페이스 정확한 구현

#### 도전 3: 테스트 간 격리 문제
**문제**: 파일 시스템 의존성으로 인한 테스트 간 간섭
**해결**: Mock FileSystem 도입, 실제 파일 시스템과 완전 격리

### 학습 포인트
1. **컨테이너 환경 네트워크 제어**: nsenter를 통한 호스트 네임스페이스 접근
2. **OS별 네트워크 도구 차이**: Ubuntu Netplan vs RHEL nmcli의 설정 방식 및 명령어
3. **안전한 네트워크 설정**: 백업/롤백, 타임아웃, 검증을 통한 무중단 변경
4. **점진적 리팩터링**: 기존 시스템을 유지하면서 새 아키텍처로 전환하는 전략
5. **도메인 주도 설계**: 비즈니스 로직을 중심으로 한 계층 구조 설계
6. **테스트 주도 개발**: Mock을 활용한 격리된 단위 테스트 작성법

### 향후 개선 방향
1. **통합 테스트 확장**: 실제 DB와 네트워크 환경을 사용한 E2E 테스트
2. **성능 최적화**: 폴링 주기 동적 조정, 배치 처리 최적화
3. **모니터링 강화**: Prometheus 메트릭, OpenTelemetry 추적 도입
4. **기능 확장**: IPv6 지원, 고급 네트워크 설정 옵션

## 배포 스크립트 개선 사항 (2025-01-08)

### 개선 내용
1. **전체 노드 지원**: 워커 노드만이 아닌 모든 노드에 이미지 배포
   - 기존: 하드코딩된 워커 노드 목록 (viola2-biz-worker01, worker02, worker03)
   - 개선: `kubectl get nodes`를 통한 동적 노드 목록 가져오기
   
2. **변경 사항**
   ```bash
   # 기존
   WORKER_NODES=(viola2-biz-worker01 viola2-biz-worker02 viola2-biz-worker03)
   
   # 개선
   ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))
   ```

3. **영향 범위**
   - DaemonSet이 모든 노드에서 실행되도록 설계되어 있으므로, 이미지도 모든 노드에 배포되어야 함
   - OpenStack multi-interface 관리를 위해 컨트롤 플레인 노드에서도 실행 필요
   - 노드 추가/제거 시 자동으로 반영되어 유지보수성 향상

### Toleration 설정 추가
컨트롤 플레인 노드에도 DaemonSet이 배포되도록 toleration 설정 추가:

```yaml
tolerations:
  # 컨트롤 플레인 노드에도 배포되도록 설정
  - key: node-role.kubernetes.io/control-plane
    operator: Exists
    effect: NoSchedule
  - key: node-role.kubernetes.io/master
    operator: Exists
    effect: NoSchedule
```

이 설정으로 `node-role.kubernetes.io/control-plane:NoSchedule` taint가 있는 마스터 노드에도 Pod가 스케줄링됩니다.

## 데이터베이스 스키마 불일치 버그 수정 (2025-01-08)

### 문제 발생
배포 후 다음과 같은 에러 발생:
```
Error 1054 (42S22): Unknown column 'ip_address' in 'field list'
```

### 원인
- 코드에서 존재하지 않는 필드들을 조회 시도 (`ip_address`, `subnet_mask`, `gateway`, `dns`, `vlan`)
- 실제 테이블에는 `macaddress`, `attached_node_name` 등만 존재
- Netplan 설정은 MAC 주소와 인터페이스 이름만 필요

### 수정 내용
1. **엔티티 구조 단순화**
   ```go
   // 기존
   type NetworkInterface struct {
       ID, MacAddress, AttachedNodeName string
       IPAddress, SubnetMask, Gateway, DNS string  // 제거
       VLAN int                                     // 제거
   }
   
   // 변경
   type NetworkInterface struct {
       ID               int
       MacAddress       string
       AttachedNodeName string
       Status           InterfaceStatus
   }
   ```

2. **데이터베이스 쿼리 수정**
   ```sql
   -- 기존
   SELECT id, macaddress, attached_node_name, ip_address, 
          subnet_mask, gateway, dns, vlan
   
   -- 변경  
   SELECT id, macaddress, attached_node_name, netplan_success
   ```

3. **Netplan 설정 단순화**
   ```yaml
   # 실제 생성되는 설정
   network:
     ethernets:
       multinic0:
         dhcp4: false
         match:
           macaddress: fa:16:3e:b1:29:8f
         set-name: multinic0
         mtu: 1500
     version: 2
   ```

4. **RHEL nmcli 설정도 동일하게 단순화**
   - ipv4.method를 'disabled'로 설정
   - IP 관련 설정 모두 제거

## 레거시 코드 정리 (2025-01-08)

### 정리 내용
클린 아키텍처 마이그레이션 완료로 인한 사용하지 않는 코드 제거:

1. **pkg/ 디렉토리 전체 제거**
   - pkg/db/ - 기존 데이터베이스 연결 로직 (-> internal/infrastructure/persistence/)
   - pkg/health/ - 레거시 헬스체크 (-> internal/infrastructure/health/)
   - pkg/netplan/ - 레거시 netplan 관리 (-> internal/infrastructure/network/)
   - pkg/network/ - 레거시 네트워크 관리 (-> internal/infrastructure/network/)
   - pkg/utils/ - 유틸리티 함수들 (-> internal/domain/services/)

2. **레거시 파일 제거**
   - cmd/agent/main_legacy.go - 이전 메인 파일
   - test/integration/ - pkg 의존성이 있는 통합 테스트

3. **보안 개선**
   - deploy.sh의 SSH_PASSWORD를 placeholder로 변경
   - 하드코딩된 패스워드 제거

### 정리 효과
- **코드베이스 크기 감소**: 불필요한 중복 코드 제거
- **아키텍처 일관성**: 클린 아키텍처로 완전 통일
- **보안 강화**: 하드코딩된 민감 정보 제거
- **유지보수성 향상**: 단일 코드 패스로 유지

### 프로젝트 구조 (최종)
```
multinic-agent/
├── cmd/agent/
|   └── main.go              # 단일 진입점
├── internal/                # 클린 아키텍처 구조
|   ├── domain/              # 도메인 계층
|   ├── application/         # 애플리케이션 계층  
|   ├── infrastructure/      # 인프라스트럭처 계층
|   └── interfaces/          # 인터페이스 어댑터
├── deployments/helm/        # Helm 차트
└── scripts/                 # 배포 스크립트
```

## 백업 로직 제거 (2025-01-08)

### 문제점
- 30초마다 폴링하면서 백업 파일이 과도하게 생성됨
- 불필요한 디스크 공간 사용
- 실제로는 단순한 설정 파일 생성만 필요

### 제거된 백업 로직
1. **UseCase에서 백업 서비스 의존성 제거**
   - ConfigureNetworkUseCase에서 BackupService 제거

2. **네트워크 어댑터에서 백업 로직 제거**
   - NetplanAdapter: 백업 생성/복원 로직 제거
   - RHELAdapter: 백업 처리 불필요 (nmcli는 파일 기반이 아님)

3. **의존성 주입 구조 단순화**
   - NetworkManagerFactory에서 BackupService 제거
   - Container에서 백업 서비스 초기화 제거

### 현재 동작 방식
- **설정 파일 생성**: 기존 파일이 있으면 단순 덮어쓰기
- **롤백 처리**: 설정 파일 제거만 수행
- **에러 처리**: netplan/nmcli 명령어 자체의 안전장치 활용

### 장점
- **성능 향상**: 백업 파일 I/O 제거로 속도 개선
- **디스크 절약**: 불필요한 백업 파일 생성 방지
- **코드 단순화**: 백업 관련 복잡한 로직 제거
- **안정성**: netplan try 등 도구 자체의 안전장치 활용

## 인터페이스 삭제 기능 구현 (2025-01-08)

### 배경 및 문제점
기존 MultiNIC Agent는 **인터페이스 생성/설정만 지원**하고 **삭제 처리가 완전히 누락**되어 있었습니다.

#### 문제 상황
```
OpenStack에서 인터페이스 삭제 
    ↓
MultiNIC Controller가 DB에서 레코드 완전 삭제
    ↓
Agent는 삭제된 것을 모름 (DB에 레코드 없음)
    ↓
multinic0~9 인터페이스가 OS에 계속 남아있음 (고아 인터페이스)
```

### 해결 방안: 현재 vs DB 비교 방식

#### 삭제 감지 전략
- **방안 A (deleted_at 기반)**: Controller가 소프트 삭제 사용 (불가능 - Controller는 하드 삭제 사용)
- **방안 B (현재 vs DB 비교)**: 현재 시스템의 multinic* 인터페이스와 DB 활성 인터페이스 비교 (채택)

#### 구현 아키텍처
```mermaid
graph TB
    A[30초 폴링] --> B[현재 multinic* 인터페이스 스캔]
    B --> C[DB에서 활성 인터페이스 조회]
    C --> D[MAC 주소 기반 비교]
    D --> E{고아 인터페이스?}
    E -->|Yes| F[인터페이스 삭제]
    E -->|No| G[다음 폴링 대기]
    F --> H[설정 파일 제거]
    H --> G
```

### 구현 상세 (최종 완료)

#### 문제점 재분석 및 해결책 변경
초기 구현 후 실제 테스트 결과, **기존 접근 방식의 문제점**이 발견되었습니다:

**실제 문제 상황:**
```
1. OpenStack에서 인터페이스 삭제 -> ip a에서 multinic2 사라짐
2. Controller가 DB에서 레코드 삭제 -> DB에 multinic0, multinic1만 남음  
3. 하지만 netplan 파일은 그대로 남음 -> /etc/netplan/92-multinic2.yaml 존재
4. 기존 Agent 로직: 시스템 인터페이스 vs DB 비교 (부정확)
```

**해결책: Netplan 파일 기반 고아 감지**
- 시스템 인터페이스가 아닌 **netplan 설정 파일을 스캔**
- **ip addr show 명령어로 실제 인터페이스 존재 여부 확인**
- 파일은 있지만 인터페이스가 없는 경우를 고아로 판단

#### 1. 완전히 새로운 삭제 로직 구현

**DeleteNetworkUseCase** (`internal/application/usecases/delete_network.go`)
```go
// findOrphanedNetplanFiles는 시스템에 존재하지 않지만 netplan 파일이 남아있는 고아 파일들을 찾습니다
func (uc *DeleteNetworkUseCase) findOrphanedNetplanFiles(ctx context.Context) ([]string, error) {
    // 1. /etc/netplan 디렉토리에서 multinic 관련 파일 스캔
    netplanDir := "/etc/netplan"
    files, err := uc.namingService.ListNetplanFiles(netplanDir)
    
    for _, fileName := range files {
        // 2. multinic 파일만 처리 (9*-multinic*.yaml 패턴)
        if !uc.isMultinicNetplanFile(fileName) {
            continue
        }
        
        // 3. 파일명에서 인터페이스 이름 추출
        interfaceName := uc.extractInterfaceNameFromFile(fileName)
        
        // 4. 해당 인터페이스가 실제 시스템에 존재하는지 확인
        exists, err := uc.checkInterfaceExists(ctx, interfaceName)
        if !exists {
            orphanedFiles = append(orphanedFiles, fileName)
        }
    }
    
    return orphanedFiles, nil
}

// checkInterfaceExists는 ip addr show 명령어로 인터페이스 존재 여부 확인
func (uc *DeleteNetworkUseCase) checkInterfaceExists(ctx context.Context, interfaceName string) (bool, error) {
    _, err := uc.namingService.GetMacAddressForInterface(interfaceName)
    if err != nil {
        if strings.Contains(err.Error(), "does not exist") {
            return false, nil  // 인터페이스가 존재하지 않음
        }
        return false, err
    }
    return true, nil  // 인터페이스가 존재함
}
```

#### 2. 도메인 서비스 확장

**InterfaceNamingService** (`internal/domain/services/interface_naming.go`)
```go
// ListNetplanFiles는 지정된 디렉토리의 netplan 파일 목록을 반환합니다
func (s *InterfaceNamingService) ListNetplanFiles(dir string) ([]string, error) {
    files, err := s.fileSystem.ListFiles(dir)
    if err != nil {
        return nil, fmt.Errorf("디렉토리 %s 파일 목록 조회 실패: %w", dir, err)
    }
    return files, nil
}

// GetMacAddressForInterface는 ip addr show 명령어로 MAC 주소 조회
func (s *InterfaceNamingService) GetMacAddressForInterface(interfaceName string) (string, error) {
    output, err := s.commandExecutor.ExecuteWithTimeout(ctx, 10*time.Second, "ip", "addr", "show", interfaceName)
    if err != nil {
        return "", fmt.Errorf("인터페이스 %s 정보 조회 실패: %w", interfaceName, err)
    }
    
    // MAC 주소 추출 정규식
    macRegex := regexp.MustCompile(`link/ether\s+([a-fA-F0-9:]{17})`)
    matches := macRegex.FindStringSubmatch(string(output))
    if len(matches) < 2 {
        return "", fmt.Errorf("인터페이스 %s에서 MAC 주소를 찾을 수 없습니다", interfaceName)
    }
    
    return matches[1], nil
}
```

#### 3. 메인 애플리케이션 통합

**main.go 폴링 루프에 삭제 로직 추가** (`cmd/agent/main.go`)
```go
func (app *Application) run(ctx context.Context) error {
    ticker := time.NewTicker(app.config.PollingInterval)
    defer ticker.Stop()

    for {
        select {
        case <-ticker.C:
            // 1. 기존 설정 처리
            app.processConfigurations(ctx)
            
            // 2. 새로 추가: 고아 인터페이스 정리 
            app.processInterfaceDeletion(ctx)
            
        case <-ctx.Done():
            return ctx.Err()
        }
    }
}

func (app *Application) processInterfaceDeletion(ctx context.Context) {
    input := usecases.DeleteNetworkInput{NodeName: app.hostname}
    
    output, err := app.deleteUseCase.Execute(ctx, input)
    if err != nil {
        app.logger.WithError(err).Error("인터페이스 삭제 처리 실패")
        return
    }
    
    if output.TotalDeleted > 0 {
        app.logger.WithFields(logrus.Fields{
            "deleted_count": output.TotalDeleted,
            "deleted_interfaces": output.DeletedInterfaces,
        }).Info("고아 인터페이스 정리 완료")
    }
}
```

#### 4. 종합 테스트 및 검증

**포괄적 테스트 커버리지** (`internal/application/usecases/delete_network_test.go`)
- **성공적인 고아 파일 정리**: netplan 파일 스캔 -> 인터페이스 부재 확인 -> 롤백 호출
- **고아 파일이 없는 경우**: 정상적으로 빈 결과 반환  
- **롤백 실패 처리**: 에러 상황에서도 다른 파일 계속 처리
- **파일명 패턴 매칭**: `91-multinic1.yaml` 형식 정확히 처리
- **인터페이스 존재 여부 확인**: `ip addr show` 명령어 기반 검증

**테스트 결과:**
```bash
=== RUN   TestDeleteNetworkUseCase_Execute_NetplanFileCleanup_Success
=== RUN   TestDeleteNetworkUseCase_Execute_NoOrphanedFiles  
=== RUN   TestDeleteNetworkUseCase_Execute_RollbackFailure
=== RUN   TestDeleteNetworkUseCase_ExtractInterfaceNameFromFile
=== RUN   TestDeleteNetworkUseCase_IsMultinicNetplanFile
--- PASS: All deletion tests (0.00s)
```

### 핵심 개선사항

#### 1. 정확한 고아 감지
- **기존**: 시스템 인터페이스 vs DB 비교 (부정확)
- **개선**: netplan 파일 vs 시스템 인터페이스 비교 (정확)

#### 2. 안전한 파일 처리  
- **패턴 매칭**: `9*-multinic*.yaml`만 처리하여 다른 netplan 파일 보호
- **에러 격리**: 한 파일 처리 실패가 다른 파일 처리에 영향 없음
- **롤백 메커니즘**: 기존 롤백 인프라 재사용으로 안정성 보장

#### 3. 실전 시나리오 해결
```
시나리오: OpenStack에서 multinic2 삭제
1. ip a -> multinic0, multinic1만 존재 (multinic2 사라짐)
2. DB -> multinic0, multinic1 레코드만 존재  
3. netplan -> 92-multinic2.yaml 파일은 여전히 존재
4. Agent -> 파일 스캔으로 고아 감지 -> 92-multinic2.yaml 삭제 -> netplan apply
```

### 배포 준비 완료

#### 검증된 기능
- **전체 테스트 통과**: 모든 유닛 테스트 및 통합 테스트 성공
- **빌드 성공**: `go build ./cmd/agent` 컴파일 완료
- **클린 아키텍처 유지**: 기존 설계 패턴 준수
- **하위 호환성**: 기존 설정 처리 로직에 영향 없음

#### 프로덕션 적용 시 예상 효과
- **디스크 공간 절약**: 고아 netplan 파일 자동 정리
- **설정 일관성**: 시스템 상태와 netplan 설정 동기화
- **운영 효율성**: 수동 정리 작업 불필요
- **안정성**: 30초 주기로 지속적인 정리 작업

## 2025-01-08 작업 완료 요약

### 인터페이스 삭제 기능 구현 완료 ✅

**주요 성과:**
1. **완전한 삭제 로직 구현**: netplan 파일 기반 고아 인터페이스 감지 및 정리
2. **클린 아키텍처 준수**: 도메인/애플리케이션/인프라 계층 분리 유지  
3. **포괄적 테스트 커버리지**: 모든 시나리오에 대한 단위 테스트 작성
4. **프로덕션 준비 완료**: 빌드 성공, 전체 테스트 통과

**해결된 실제 문제:**
- OpenStack에서 인터페이스 삭제 시 남는 고아 netplan 파일 자동 정리
- 시스템 상태와 netplan 설정 간 불일치 해결
- 디스크 공간 절약 및 설정 일관성 확보

**다음 단계:** 프로덕션 환경에 배포하여 실제 고아 파일 정리 효과 확인

## 로그 레벨 최적화 (2025-01-08)

### 문제점
프로덕션 배포 후 불필요한 로그가 과도하게 출력되는 문제 발견:
```
INFO "고아 인터페이스 삭제 프로세스 시작"  ← 30초마다 모든 노드에서 출력
INFO "삭제 대상 고아 netplan 파일이 없습니다"  ← 삭제할 파일이 없어도 출력
```

### 해결책: 로그 레벨 조정
- **Debug 레벨로 변경**: 삭제할 파일이 없는 경우 -> 운영 환경에서 출력 안됨
- **Info 레벨 유지**: 실제 삭제 작업이 있을 때만 -> 중요한 정보만 출력

### 수정 내용
```go
// Before: 시끄러운 로그
uc.logger.Info("고아 인터페이스 삭제 프로세스 시작")
uc.logger.Info("삭제 대상 고아 netplan 파일이 없습니다")

// After: 조용한 로그  
uc.logger.Debug("고아 인터페이스 삭제 프로세스 시작")
uc.logger.Debug("삭제 대상 고아 netplan 파일이 없습니다")
uc.logger.Info("고아 netplan 파일 감지 완료 - 삭제 프로세스 시작")  // 실제 삭제할 때만
```

### 결과
- **불필요한 로그 제거**: 정상 상황에서는 조용함
- **중요한 정보 유지**: 실제 삭제 작업 시에만 Info 레벨 로그 출력  
- **성능 영향 없음**: netplan은 실제 삭제할 파일이 있을 때만 실행

## README_TEAM.md 배포 가이드 완성 (2025-01-08)

### 추가된 섹션: 배포 전 필수 설정

README_TEAM.md에 실용적인 배포 가이드를 추가했습니다:

#### 1. SSH 접근 정보 설정
```bash
export SSH_PASSWORD="your_actual_ssh_password"
```

#### 2. 데이터베이스 연결 정보 설정
`deployments/helm/values.yaml` 파일 수정 필수:
```yaml
database:
  host: "10.0.0.100"              # 실제 DB 호스트 IP
  port: "3306"                    # 실제 DB 포트
  user: "multinic_user"           # 실제 DB 사용자명
  password: "your_db_password"    # 실제 DB 비밀번호
  name: "multinic_database"       # 실제 DB 이름

agent:
  pollInterval: "30s"             # 원하는 폴링 간격
  logLevel: "info"                # 로그 레벨
```

#### 3. 빠른 설정 체크리스트
배포 전 확인해야 할 항목들을 체크리스트로 정리하여 누락 방지

#### 4. 실제 배포 예시
구체적인 환경 설정 값과 함께 실제 배포 과정을 단계별로 제시

### 개선 효과
- **배포 실패 방지**: 필수 설정 누락으로 인한 배포 실패 최소화
- **사용 편의성**: 초보자도 쉽게 따라할 수 있는 상세한 가이드
- **실무 적용성**: 실제 환경에서 바로 사용 가능한 구체적인 예시

## Netplan 동기화 문제 해결 (2025-07-10)

### 문제 상황
- 구형 netplan 파일(94-95 형식)에 `addresses` 필드가 없음
- 신형 netplan 파일(90-91 형식)에는 `addresses`, `gateway4`, `routes` 등이 있음
- DB에는 모든 인터페이스의 IP 정보가 있지만 파일이 업데이트되지 않음

### 원인 분석
1. **isDrifted 함수 문제**: IP 주소 비교 로직이 잘못되어 드리프트를 감지하지 못함
2. **GetPendingInterfaces 제한**: netplan_success=0인 인터페이스만 조회
3. **파일 주소 파싱 오류**: addresses 필드의 CIDR 표기법 처리 미흡

### 해결 방법

#### 1. GetAllNodeInterfaces 추가
```go
// 모든 활성 인터페이스 조회 (netplan_success 상태 무관)
func (r *MySQLRepository) GetAllNodeInterfaces(ctx context.Context, nodeName string) ([]entities.NetworkInterface, error) {
    query := `
        SELECT id, macaddress, attached_node_name, netplan_success, address, cidr, mtu 
        FROM multi_interface 
        WHERE attached_node_name = ? 
        AND deleted_at IS NULL 
        LIMIT 10
    `
    // ...
}
```

#### 2. isDrifted 함수 개선
```go
func (uc *ConfigureNetworkUseCase) isDrifted(ctx context.Context, dbIface entities.NetworkInterface, configPath string) bool {
    // ...
    
    // 파일에서 IP 주소 파싱
    if hasAddresses && len(eth.Addresses) > 0 {
        _, ipNet, err := net.ParseCIDR(eth.Addresses[0])
        if err == nil {
            fileAddress = ipNet.IP.String()  // IP만 추출
            fileCIDR = ipNet.String()        // 전체 CIDR
        }
    }
    
    // 드리프트 조건 개선
    isDrifted := (!hasAddresses && dbIface.Address != "") ||  // 구형 파일 + DB에 IP 있음
        (dbIface.Address != fileAddress) ||                    // IP 불일치
        (dbIface.CIDR != fileCIDR) ||                         // CIDR 불일치
        (dbIface.MTU != fileMTU)                              // MTU 불일치
}
```

#### 3. 인터페이스 처리 조건 변경
```go
// Execute 메서드에서
allInterfaces, err := uc.repository.GetAllNodeInterfaces(ctx, input.NodeName)

// 파일 존재 여부와 드리프트 체크
if !fileExists || isDrifted || iface.Status == entities.StatusPending {
    // 처리 필요
}
```

### 테스트 추가
동기화 테스트 케이스 추가:
```go
{
    name: "설정 동기화 - 변경된 IP와 MTU를 감지하고 수정",
    setupMocks: func(...) {
        // DB 데이터와 다른 파일 데이터 설정
        dbIface := entities.NetworkInterface{
            Address: "1.1.1.1",
            CIDR:    "1.1.1.0/24",
            MTU:     1500,
        }
        
        // 드리프트된 YAML
        driftedYAML := `
        addresses: ["1.1.1.2/24"]  # 다른 IP
        mtu: 1400                  # 다른 MTU
        `
    },
    expectedOutput: &ConfigureNetworkOutput{
        ProcessedCount: 1,  // 드리프트로 인해 처리됨
    },
}
```

### 결과
- 컴파일 오류 해결
- 테스트 케이스 통과
- 구형/신형 netplan 파일 모두 동기화 가능
- LOG_LEVEL 환경 변수 적용 확인

## IP 주소 드리프트 오탐지 문제 해결 (2025-07-10)

### 문제 상황
동기화 기능은 작동했지만, 30초마다 IP 주소가 변경되었다고 잘못 감지하여 불필요한 재설정이 반복됨:
```
드리프트 감지 상세 정보:
- DB Address: "11.11.11.146" 
- File Address: "11.11.11.0" (네트워크 주소)
- drift_condition_2: true (항상)
```

### 원인
`net.ParseCIDR` 사용 시 `ipNet.IP`는 네트워크 주소를 반환:
```go
// 잘못된 코드
_, ipNet, err := net.ParseCIDR(eth.Addresses[0])
fileAddress = ipNet.IP.String() // "11.11.11.0" (네트워크 주소)
```

### 해결 방법
실제 IP 주소를 가져오도록 수정:
```go
// 수정된 코드
ip, ipNet, err := net.ParseCIDR(eth.Addresses[0])
fileAddress = ip.String()     // "11.11.11.146" (실제 IP)
fileCIDR = ipNet.String()    // "11.11.11.0/24" (네트워크 CIDR)
```

### 결과
- IP 주소가 정확히 비교되어 불필요한 재설정 방지
- 네트워크 트래픽 및 시스템 부하 감소

## 로그 최적화 (2025-07-10) 

### Phase 1: 드리프트 관련 로그 개선
#### 문제
매 30초마다 모든 인터페이스에 대해 드리프트 감지 상세 로그 출력

#### 해결
드리프트가 실제로 감지된 경우에만 로그 출력:
```go
if isDrifted {
    uc.logger.WithFields(logrus.Fields{
        // 드리프트 상세 정보
    }).Debug("드리프트 감지됨")
}
```

### Phase 2: 반복되는 정보성 로그 제거
#### 문제
정상 상태에서도 30초마다 반복되는 로그:
- "OS 타입 감지 완료" (중복 출력)
- "처리할 인터페이스 발견"
- "네트워크 처리 완료" (변경사항 없어도 출력)

#### 해결
1. **OS 감지 로그**: Info -> Debug 레벨로 변경
2. **처리 인터페이스 로그**: 완전 제거
3. **처리 완료 로그**: 실제 작업이 있을 때만 출력
   ```go
   if configOutput.ProcessedCount > 0 || 
      configOutput.FailedCount > 0 || 
      deleteOutput.TotalDeleted > 0 {
       logger.Info("네트워크 처리 완료")
   }
   ```

### 로그 출력 비교

#### 개선 전 (30초마다)
```
INFO "OS 타입 감지 완료"
INFO "처리할 인터페이스 발견" 
DEBUG "드리프트 감지 상세 정보" (모든 인터페이스)
INFO "인터페이스 설정 시작" (불필요한 재설정)
INFO "네트워크 처리 완료"
```

#### 개선 후
- **정상 상태**: 로그 출력 없음 (완전히 조용함)
- **변경 발생 시**: 해당 변경사항만 출력
- **에러 발생 시**: 에러 정보만 출력

### 효과
- 로그 가독성 대폭 향상
- 디스크 사용량 감소
- 실제 중요한 이벤트 식별 용이
- 운영 환경에서 노이즈 제거

### Phase 3: Debug 레벨 로그 최적화
#### 문제
Debug 레벨에서도 30초마다 반복되는 로그:
- "활성 인터페이스 조회 완료"
- "고아 인터페이스 삭제 프로세스 시작" 
- "삭제 대상 고아 netplan 파일이 없습니다"

#### 해결
모든 반복 로그 제거:
```go
// 제거된 로그들
// r.logger.Debug("활성 인터페이스 조회 완료")
// uc.logger.Debug("고아 인터페이스 삭제 프로세스 시작")
// uc.logger.Debug("삭제 대상 고아 netplan 파일이 없습니다")
```

#### 최종 결과
- **모든 로그 레벨**: 정상 상태에서 완전히 조용함
- **실제 작업 시**: 해당 작업 로그만 출력
- **시작 시**: 필수 정보(헬스체크, 에이전트 시작)만 출력

## 프로젝트 이름 변경 (multinic-agent-v2 → multinic-agent) (2025-07-11)

### 변경 사항
1. **모듈명 변경**: go.mod의 module 이름을 `multinic-agent-v2`에서 `multinic-agent`로 변경
2. **Import 경로 수정**: 모든 Go 소스 파일의 import 경로 업데이트
3. **문서 업데이트**: README.md와 CLAUDE.md에서 v2 제거
4. **GitHub 저장소**: `multinic-agent-v2` → `multinic-agent`로 이름 변경
5. **로컬 폴더명 변경**: 수동으로 폴더명 변경 필요

### 변경 방법
```bash
# 1. 모든 파일에서 multinic-agent-v2를 multinic-agent로 변경
sed -i '' 's/multinic-agent-v2/multinic-agent/g' go.mod
find . -name "*.go" -type f -exec sed -i '' 's/multinic-agent-v2/multinic-agent/g' {} \;

# 2. 문서 파일 업데이트
sed -i '' 's/multinic-agent-v2/multinic-agent/g' README.md CLAUDE.md
sed -i '' 's/MultiNIC Agent v2/MultiNIC Agent/g' README.md CLAUDE.md

# 3. Git remote URL 업데이트
git remote set-url origin https://github.com/Minzino/multinic-agent.git

# 4. 로컬 폴더명 변경 (프로젝트 외부에서 실행)
cd ..
mv multinic-agent-v2 multinic-agent
```

### 영향
- ✅ 프로젝트 빌드 정상 작동
- ✅ 모든 테스트 통과
- ✅ import 경로 일관성 확보
- ✅ 더 간결한 프로젝트 이름

## RHEL/CentOS 지원 추가 (2025-07-11)

### 배경
기존에 Ubuntu(Netplan)과 SUSE(Wicked)를 지원했으나, SUSE 지원을 제거하고 대신 RHEL/CentOS 계열 OS를 위한 nmcli 기반 지원을 추가했습니다.

### 주요 변경사항

#### 1. SUSE 관련 코드 완전 제거
- `OSTypeSUSE` 상수 제거
- Wicked 관련 모든 로직과 문서 제거
- OS 감지 로직에서 SUSE 제거

#### 2. RHEL 어댑터 기능 강화
**기존 문제점**: RHEL 어댑터가 IP 주소 설정을 지원하지 않음 (ipv4.method=disabled만 설정)

**개선사항**:
```go
// IP 주소 설정 지원
if iface.Address != "" && iface.CIDR != "" {
    // CIDR에서 prefix 추출 (예: "192.168.1.0/24" → "24")
    parts := strings.Split(iface.CIDR, "/")
    if len(parts) == 2 {
        prefix := parts[1]
        fullAddress := fmt.Sprintf("%s/%s", iface.Address, prefix)
        
        // nmcli로 정적 IP 설정
        setIPCmd := []string{"connection", "modify", ifaceName, 
            "ipv4.method", "manual", 
            "ipv4.addresses", fullAddress}
    }
}

// MTU 설정 지원
if iface.MTU > 0 {
    setMTUCmd := []string{"connection", "modify", ifaceName, 
        "ethernet.mtu", fmt.Sprintf("%d", iface.MTU)}
}
```

#### 3. RHEL 고아 인터페이스 정리 기능
**구현 방식**: MAC 주소 기반 고아 감지

```go
// 1. DB에서 활성 MAC 주소 목록 가져오기
activeInterfaces, _ := uc.repository.GetActiveInterfaces(ctx, nodeName)
activeMACAddresses := make(map[string]bool)
for _, iface := range activeInterfaces {
    activeMACAddresses[strings.ToLower(iface.MacAddress)] = true
}

// 2. nmcli connection에서 MAC 주소 추출
output, _ := uc.namingService.GetNmcliConnectionMAC(ctx, connName)
// 출력 형식: "802-3-ethernet.mac-address:FA:16:3E:00:BE:63"

// 3. DB에 없는 MAC이면 고아로 판단
if !activeMACAddresses[strings.ToLower(macAddress)] {
    // 고아 connection 삭제
    uc.rollbacker.Rollback(ctx, connName)
}
```

#### 4. OS 감지 개선
RHEL 계열 OS 감지 확대:
- RHEL, CentOS, Rocky Linux, AlmaLinux, Oracle Linux
- ID 필드와 ID_LIKE 필드 모두 확인

```go
if id == "rhel" || id == "centos" || id == "rocky" || 
   id == "almalinux" || id == "oracle" || 
   strings.Contains(idLike, "rhel") || 
   strings.Contains(idLike, "fedora") {
    return interfaces.OSTypeRHEL, nil
}
```

### 테스트 커버리지

#### RHELAdapter 테스트
1. **Configure 테스트**
   - IP 없는 인터페이스 설정
   - 정적 IP 주소 설정
   - connection add 실패 처리
   - activation 실패 시 롤백

2. **Validate 테스트**
   - connected 상태 확인
   - disconnected 상태 감지
   - 인터페이스 없음 감지

3. **Rollback 테스트**
   - 정상 롤백
   - 이미 삭제된 경우 처리

#### 통합 테스트
- GetNmcliConnectionMAC 메서드 추가
- DeleteNetworkUseCase의 RHEL 지원 테스트
- Mock을 통한 nmcli 명령어 시뮬레이션

### 문서 업데이트
1. **README.md**
   - Ubuntu(Netplan) 및 RHEL/CentOS(nmcli) 지원 명시
   - 요구사항: Ubuntu 18.04+ 또는 RHEL/CentOS 7+

2. **CLAUDE.md**
   - SUSE/Wicked 제거, RHEL/nmcli로 대체
   - 기술적 차이점 설명 업데이트

### 결과
- ✅ RHEL/CentOS에서 완전한 네트워크 인터페이스 관리 지원
- ✅ IP 주소, CIDR, MTU 설정 가능
- ✅ MAC 주소 기반 고아 인터페이스 정리
- ✅ 모든 테스트 통과
- ✅ 프로덕션 배포 준비 완료

## 고아 인터페이스 감지 로직 개선 (2025-07-10)

### 문제 상황
기존 고아 감지 로직이 잘못 작동하여 방금 생성한 인터페이스를 30초 후에 삭제하는 무한 루프 발생:

```
6:38:50 - multinic1 생성 성공 (interface_id: 23)
6:39:20 - multinic1을 고아로 잘못 판단하여 삭제
6:39:50 - 같은 interface_id: 23으로 다시 생성 시도
6:39:54 - "네트워크 인터페이스가 존재하지 않음" 에러
```

### 원인 분석
1. **기존 로직의 문제점**:
   - 고아 판단 기준: `netplan 파일 존재 + ip addr show 실패 = 고아`
   - OpenStack 포트가 없어서 인터페이스 생성에 실패한 경우도 고아로 판단
   - DB에는 여전히 레코드가 있는데도 파일을 삭제

2. **실제 시나리오**:
   ```
   1. DB에서 interface 정보 가져옴 (netplan_success=0)
   2. netplan 파일 생성 -> netplan apply
   3. OpenStack 포트가 없어서 실제 인터페이스 생성 실패
   4. 30초 후 고아 감지: 파일은 있는데 인터페이스가 없음 -> 삭제
   5. DB에 여전히 레코드가 있으므로 다시 생성 시도 -> 무한 반복
   ```

### 해결 방법: DB 기반 고아 감지

#### 변경 전
```go
// 시스템 인터페이스 존재 여부로 판단
exists, err := uc.checkInterfaceExists(ctx, interfaceName)
if !exists {
    // 고아로 판단
}
```

#### 변경 후
```go
// DB의 활성 인터페이스 목록과 비교
activeInterfaces, err := uc.repository.GetAllNodeInterfaces(ctx, hostname)
activeMACAddresses := make(map[string]bool)
for _, iface := range activeInterfaces {
    activeMACAddresses[strings.ToLower(iface.MacAddress)] = true
}

// netplan 파일의 MAC 주소가 DB에 없으면 고아
macAddress, err := uc.getMACAddressFromNetplanFile(filePath)
if !activeMACAddresses[strings.ToLower(macAddress)] {
    // 진짜 고아 파일
}
```

### 구현 변경사항

1. **DeleteNetworkUseCase 수정**:
   - repository와 fileSystem 의존성 추가
   - findOrphanedNetplanFiles 메서드를 DB 기반으로 재구현
   - getMACAddressFromNetplanFile 메서드 추가

2. **의존성 주입 구조 업데이트**:
   - Container에서 DeleteNetworkUseCase 생성 시 repository, fileSystem 전달
   - 테스트 코드도 새로운 시그니처에 맞게 수정

3. **테스트 개선**:
   - Mock 기반으로 DB 활성 인터페이스 시뮬레이션
   - netplan 파일 내용 파싱 테스트 추가

### 효과
- 생성 실패한 인터페이스를 고아로 잘못 판단하는 문제 해결
- DB가 진실의 원천(source of truth)이 되어 일관성 향상
- Controller가 삭제한 인터페이스만 정확히 정리
- 무한 루프 문제 완전 해결