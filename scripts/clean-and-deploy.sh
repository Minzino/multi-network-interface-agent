#!/bin/bash

set -e

# 색상 정의
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🧹 이미지 정리 및 재배포 스크립트${NC}"

# 변수 설정
IMAGE_NAME=${IMAGE_NAME:-"multinic-agent"}
IMAGE_TAG=${IMAGE_TAG:-"0.5.0"}
SSH_PASSWORD=${SSH_PASSWORD:-"YOUR_SSH_PASSWORD"}

# 모든 노드 목록
ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))

echo -e "${BLUE}📋 대상 노드: ${ALL_NODES[*]}${NC}"

# 1. 모든 노드에서 이미지 정리
echo -e "\n${BLUE}🗑️  1단계: 모든 노드에서 기존 이미지 제거${NC}"
for node in "${ALL_NODES[@]}"; do
    echo -e "${YELLOW}$node 노드 정리 중...${NC}"
    
    # podman 이미지 제거
    echo -e "  - Podman 이미지 제거 중..."
    sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no $node "
        # Podman에서 multinic 관련 이미지 모두 제거
        sudo podman images | grep -E 'multinic|MULTINIC' | awk '{print \$3}' | xargs -r sudo podman rmi -f || true
    " 2>/dev/null || echo -e "${YELLOW}  ⚠️  Podman 이미지 제거 실패 (계속 진행)${NC}"
    
    # nerdctl 이미지 제거
    echo -e "  - Nerdctl 이미지 제거 중..."
    sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no $node "
        # Nerdctl에서 multinic 관련 이미지 모두 제거
        sudo nerdctl --namespace=k8s.io images | grep -E 'multinic|MULTINIC' | awk '{print \$3}' | xargs -r sudo nerdctl --namespace=k8s.io rmi -f || true
    " 2>/dev/null || echo -e "${YELLOW}  ⚠️  Nerdctl 이미지 제거 실패 (계속 진행)${NC}"
    
    echo -e "${GREEN}✓ $node 노드 정리 완료${NC}"
done

# 2. 로컬에서도 이미지 정리
echo -e "\n${BLUE}🗑️  2단계: 로컬 이미지 정리${NC}"
echo -e "${YELLOW}로컬 Podman 이미지 제거 중...${NC}"
sudo podman images | grep -E 'multinic|MULTINIC' | awk '{print $3}' | xargs -r sudo podman rmi -f || true

echo -e "${YELLOW}로컬 Nerdctl 이미지 제거 중...${NC}"
sudo nerdctl --namespace=k8s.io images | grep -E 'multinic|MULTINIC' | awk '{print $3}' | xargs -r sudo nerdctl --namespace=k8s.io rmi -f || true

echo -e "${GREEN}✓ 로컬 이미지 정리 완료${NC}"

# 3. 배포 스크립트 실행
echo -e "\n${BLUE}🚀 3단계: 새로운 배포 시작${NC}"
cd "$(dirname "$0")"
./deploy.sh

echo -e "\n${GREEN}🎉 정리 및 재배포가 완료되었습니다!${NC}"

# 4. 이미지 확인
echo -e "\n${BLUE}📊 4단계: 이미지 상태 확인${NC}"
for node in "${ALL_NODES[@]}"; do
    echo -e "\n${YELLOW}=== $node 노드 ===${NC}"
    echo -e "${BLUE}Nerdctl 이미지:${NC}"
    sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no $node "sudo nerdctl --namespace=k8s.io images | grep -E 'multinic|NAME' || echo 'No images found'" 2>/dev/null || echo -e "${RED}접근 실패${NC}"
    
    echo -e "${BLUE}Podman 이미지:${NC}"
    sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no $node "sudo podman images | grep -E 'multinic|REPOSITORY' || echo 'No images found'" 2>/dev/null || echo -e "${RED}접근 실패${NC}"
done