.PHONY: all build test clean docker-build helm-install lint

# 변수
BINARY_NAME=multinic-agent
DOCKER_IMAGE=multinic-agent
VERSION?=latest
NAMESPACE?=default

# Go 관련 변수
GOBASE=$(shell pwd)
GOBIN=$(GOBASE)/bin
GOFILES=$(wildcard *.go)

# 빌드
all: test build

build:
	@echo ">>> 바이너리 빌드 중..."
	@go build -o $(GOBIN)/$(BINARY_NAME) cmd/agent/main.go

build-controller:
	@echo ">>> Controller 바이너리 빌드 중..."
	@go build -o $(GOBIN)/multinic-controller cmd/controller/main.go

# 테스트
test:
	@echo ">>> 테스트 실행 중..."
	@go test -v ./...

# 테스트 커버리지
test-coverage:
	@echo ">>> 테스트 커버리지 분석 중..."
	@go test -v -coverprofile=coverage.out ./...
	@go tool cover -html=coverage.out -o coverage.html

# 린트
lint:
	@echo ">>> 코드 린트 검사 중..."
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run; \
	else \
		echo "golangci-lint가 설치되지 않음. 'go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest' 실행"; \
	fi

# 종속성 다운로드
deps:
	@echo ">>> 종속성 다운로드 중..."
	@go mod download
	@go mod tidy

# Docker 빌드
docker-build:
	@echo ">>> Docker 이미지 빌드 중..."
	@docker build -t $(DOCKER_IMAGE):$(VERSION) .

# Docker 푸시
docker-push:
	@echo ">>> Docker 이미지 푸시 중..."
	@docker push $(DOCKER_IMAGE):$(VERSION)

# Helm 설치
helm-install:
	@echo ">>> Helm 차트 설치 중..."
	@helm install multinic-agent ./deployments/helm --namespace $(NAMESPACE)

# Helm 업그레이드
helm-upgrade:
	@echo ">>> Helm 차트 업그레이드 중..."
	@helm upgrade multinic-agent ./deployments/helm --namespace $(NAMESPACE)

# Helm 삭제
helm-uninstall:
	@echo ">>> Helm 차트 삭제 중..."
	@helm uninstall multinic-agent --namespace $(NAMESPACE)

# 로컬 실행
run:
	@echo ">>> 로컬에서 에이전트 실행 중..."
	@go run cmd/agent/main.go

# 청소
clean:
	@echo ">>> 빌드 파일 청소 중..."
	@rm -rf $(GOBIN)
	@rm -f coverage.out coverage.html

# 통합 테스트
integration-test:
	@echo ">>> 통합 테스트 실행 중..."
	@go test -v -tags=integration ./test/integration/...

# 벤치마크
benchmark:
	@echo ">>> 벤치마크 실행 중..."
	@go test -bench=. -benchmem ./...

# 보안 스캔
security-scan:
	@echo ">>> 보안 스캔 실행 중..."
	@if command -v gosec >/dev/null 2>&1; then \
		gosec ./...; \
	else \
		echo "gosec이 설치되지 않음. 'go install github.com/securego/gosec/v2/cmd/gosec@latest' 실행"; \
	fi

# 포맷팅
fmt:
	@echo ">>> 코드 포맷팅 중..."
	@go fmt ./...

# Vet
vet:
	@echo ">>> go vet 실행 중..."
	@go vet ./...

# 전체 검증 (CI/CD용)
verify: deps fmt vet lint test

# 도움말
help:
	@echo "사용 가능한 명령어:"
	@echo "  make build          - 바이너리 빌드"
	@echo "  make test           - 테스트 실행"
	@echo "  make test-coverage  - 테스트 커버리지 분석"
	@echo "  make lint           - 코드 린트 검사"
	@echo "  make docker-build   - Docker 이미지 빌드"
	@echo "  make helm-install   - Helm 차트 설치"
	@echo "  make run            - 로컬 실행"
	@echo "  make clean          - 빌드 파일 청소"