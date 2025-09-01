# 빌드 스테이지
FROM golang:1.24-alpine AS builder

WORKDIR /app
COPY go.mod go.sum ./
ENV GOTOOLCHAIN=auto
RUN go mod download

# 소스 코드 복사 (관련 디렉토리만 명시하여 캐시 효율성 증대)
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN go build -o multinic-agent cmd/agent/main.go \
 && go build -o multinic-controller cmd/controller/main.go

# 실행 스테이지
FROM alpine:3.18

# 필요한 도구들 설치 (nsenter는 util-linux 패키지에 포함됨)
RUN apk add --no-cache \
    ca-certificates \
    util-linux

WORKDIR /app
COPY --from=builder /app/multinic-agent .
COPY --from=builder /app/multinic-controller .

ENTRYPOINT ["./multinic-agent"]
