#!/bin/bash

set -e

# 색상 정의
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🚀 MultiNIC Agent v2 HELM-ONLY 배포 스크립트${NC}"

# 변수 설정
IMAGE_NAME=${IMAGE_NAME:-"multinic-agent"}
IMAGE_TAG=${IMAGE_TAG:-"0.5.0"}
NAMESPACE=${NAMESPACE:-"default"}
RELEASE_NAME=${RELEASE_NAME:-"multinic-agent"}

cd "$(dirname "$0")/.."

# 9. Helm 차트 검증
echo -e "\n${BLUE}📋 9단계: Helm 차트 검증${NC}"
if helm lint ./deployments/helm; then
    echo -e "${GREEN}✓ Helm 차트 검증 완료${NC}"
else
    echo -e "${RED}✗ Helm 차트 검증 실패${NC}"
    exit 1
fi

# 10. MultiNIC Agent 배포 (업그레이드 또는 신규 설치)
echo -e "\n${BLUE}🚀 10단계: MultiNIC Agent 배포${NC}"
echo -e "${YELLOW}기존 Helm 릴리즈를 정리합니다 (오류는 무시됩니다)...${NC}"
helm uninstall $RELEASE_NAME --namespace $NAMESPACE &> /dev/null || true
echo -e "${YELLOW}Helm으로 업그레이드 또는 신규 설치를 진행합니다...${NC}"
echo -e "${BLUE}추가 Helm 인자(HELM_EXTRA_ARGS): ${HELM_EXTRA_ARGS}${NC}"
if helm upgrade --install $RELEASE_NAME ./deployments/helm \
    --namespace $NAMESPACE \
    --set image.repository=docker.io/library/$IMAGE_NAME \
    --set image.tag=$IMAGE_TAG \
    --set image.pullPolicy=Never \
    ${HELM_EXTRA_ARGS} \
    --wait --timeout=5m --debug; then
    echo -e "${GREEN}✓ MultiNIC Agent 배포 완료${NC}"
else
    echo -e "${RED}✗ MultiNIC Agent 배포 실패${NC}"
    exit 1
fi

# 11. DaemonSet Pod 상태 확인
echo -e "\n${BLUE}🔍 11단계: DaemonSet Pod 상태 확인${NC}"
echo -e "${YELLOW}DaemonSet Pod들이 Ready 상태가 될 때까지 대기중...${NC}"
if kubectl wait --for=condition=Ready pod -l app.kubernetes.io/name=multinic-agent -n $NAMESPACE --timeout=300s; then
    echo -e "${GREEN}✓ 모든 Agent Pod가 성공적으로 실행중입니다${NC}"
else
    echo -e "${YELLOW}⚠️  일부 Pod의 Ready 상태 확인 타임아웃. 수동으로 확인해주세요.${NC}"
fi

echo -e "\n${GREEN}🎉 Helm 배포가 완료되었습니다!${NC}"
