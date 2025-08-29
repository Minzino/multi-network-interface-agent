#!/bin/bash

set -e

# 색상 정의
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🚀 MultiNIC Agent v1.0.0 배포 스크립트${NC}"

# 변수 설정
IMAGE_NAME=${IMAGE_NAME:-"multinic-agent"}
IMAGE_TAG=${IMAGE_TAG:-"1.0.0"}
NAMESPACE=${NAMESPACE:-"multinic-system"}
RELEASE_NAME=${RELEASE_NAME:-"multinic-agent"}
SSH_PASSWORD=${SSH_PASSWORD:-"YOUR_SSH_PASSWORD"}

# 모든 노드 목록을 동적으로 가져오기
ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))

echo -e "이미지: ${BLUE}${IMAGE_NAME}:${IMAGE_TAG}${NC}"
echo -e "네임스페이스: ${BLUE}${NAMESPACE}${NC}"
echo -e "클러스터 노드: ${BLUE}${ALL_NODES[*]}${NC}"

# 1. 네임스페이스 생성
echo -e "\n${BLUE}1. 네임스페이스 설정${NC}"
if kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -; then
    echo -e "${GREEN}✓ 네임스페이스 준비 완료${NC}"
else
    echo -e "${RED}✗ 네임스페이스 생성 실패${NC}"
    exit 1
fi

# 2. 필수 도구 확인
echo -e "\n${BLUE}2. 필수 도구 확인${NC}"
commands=("nerdctl" "helm" "kubectl" "sshpass")
for cmd in "${commands[@]}"; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}✗ $cmd가 설치되어 있지 않습니다${NC}"
        exit 1
    fi
done
echo -e "${GREEN}✓ 모든 필수 도구 확인 완료${NC}"

# 3. 이미지 빌드
echo -e "\n${BLUE}3. 이미지 빌드${NC}"
if nerdctl build -t ${IMAGE_NAME}:${IMAGE_TAG} .; then
    echo -e "${GREEN}✓ 이미지 빌드 완료: ${IMAGE_NAME}:${IMAGE_TAG}${NC}"
else
    echo -e "${RED}✗ 이미지 빌드 실패${NC}"
    exit 1
fi

# 4. 이미지를 모든 노드에 배포
echo -e "\n${BLUE}4. 모든 노드에 이미지 배포${NC}"
TMP_IMAGE_FILE="/tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar"

echo -e "${YELLOW}이미지 저장 중...${NC}"
nerdctl save ${IMAGE_NAME}:${IMAGE_TAG} -o ${TMP_IMAGE_FILE}

CURRENT_NODE=$(hostname)

for node in "${ALL_NODES[@]}"; do
    echo -e "${YELLOW}노드 ${node}에 이미지 배포 중...${NC}"
    
    if [ "$node" = "$CURRENT_NODE" ]; then
        # 현재 노드는 이미 이미지가 있으므로 건너뛰기
        echo -e "${GREEN}✓ ${node}: 현재 노드 (이미지 이미 존재)${NC}"
        continue
    fi
    
    # 이미지 파일 전송
    if sshpass -p "$SSH_PASSWORD" scp -o StrictHostKeyChecking=no ${TMP_IMAGE_FILE} root@${node}:/tmp/; then
        # 원격 노드에서 이미지 로드
        if sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no root@${node} "nerdctl load -i /tmp/$(basename ${TMP_IMAGE_FILE}) && rm /tmp/$(basename ${TMP_IMAGE_FILE})"; then
            echo -e "${GREEN}✓ ${node}: 이미지 배포 완료${NC}"
        else
            echo -e "${RED}✗ ${node}: 이미지 로드 실패${NC}"
            exit 1
        fi
    else
        echo -e "${RED}✗ ${node}: 이미지 전송 실패${NC}"
        exit 1
    fi
done

# 임시 파일 정리
rm -f ${TMP_IMAGE_FILE}

# 5. Helm 차트 배포 (CRD는 pre-install hook으로 자동 업데이트됨)
echo -e "\n${BLUE}5. Helm 차트 배포${NC}"
if helm upgrade --install $RELEASE_NAME ./deployments/helm \
    --namespace $NAMESPACE \
    --set image.tag=${IMAGE_TAG} \
    --set namespace=${NAMESPACE} \
    --wait --timeout=300s; then
    echo -e "${GREEN}✓ Helm 차트 배포 완료${NC}"
else
    echo -e "${RED}✗ Helm 차트 배포 실패${NC}"
    exit 1
fi

# 6. 배포 확인
echo -e "\n${BLUE}6. 배포 상태 확인${NC}"
sleep 5

echo -e "\n${YELLOW}Controller 상태:${NC}"
kubectl get pods -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent-controller

echo -e "\n${YELLOW}MultiNIC NodeConfig:${NC}"
kubectl get multinicnodeconfigs.multinic.io -n $NAMESPACE

echo -e "\n${GREEN}✅ 배포 완료! MultiNIC Agent v1.0.0이 성공적으로 배포되었습니다.${NC}"
echo -e "\n${BLUE}로그 확인:${NC}"
echo -e "kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent-controller -f"