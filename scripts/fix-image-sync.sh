#!/bin/bash

# 색상 정의
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}🔄 Podman에서 Nerdctl로 이미지 동기화${NC}"

IMAGE_NAME="multinic-agent"
IMAGE_TAG="0.5.0"

# 1. Podman에서 이미지 확인
echo -e "\n${BLUE}1. Podman 이미지 확인${NC}"
sudo podman images | grep $IMAGE_NAME

# 2. Podman에서 이미지 export
echo -e "\n${BLUE}2. Podman에서 이미지 export${NC}"
sudo podman save -o /tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar docker.io/library/${IMAGE_NAME}:${IMAGE_TAG}

# 3. Nerdctl로 import
echo -e "\n${BLUE}3. Nerdctl로 import${NC}"
sudo nerdctl --namespace=k8s.io load -i /tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar

# 4. 태그 정리 (필요시)
echo -e "\n${BLUE}4. 태그 정리${NC}"
# docker.io/library/multinic-agent:0.5.0 → multinic-agent:0.5.0
sudo nerdctl --namespace=k8s.io tag docker.io/library/${IMAGE_NAME}:${IMAGE_TAG} ${IMAGE_NAME}:${IMAGE_TAG}

# 5. 임시 파일 삭제
echo -e "\n${BLUE}5. 임시 파일 삭제${NC}"
rm -f /tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar

# 6. 결과 확인
echo -e "\n${BLUE}6. 최종 이미지 확인${NC}"
echo -e "${YELLOW}Nerdctl 이미지:${NC}"
sudo nerdctl --namespace=k8s.io images | grep -E "$IMAGE_NAME|NAME"

echo -e "\n${GREEN}✅ 완료!${NC}"