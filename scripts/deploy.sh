#!/bin/bash

set -e

# 색상 정의
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🚀 MultiNIC Agent v1.0.0 배포 스크립트${NC}"

# 사용법:
# SSH 패스워드 인증: SSH_PASSWORD="your_password" ./deploy.sh
# SSH Key 인증: SSH_KEY_PATH="~/.ssh/id_rsa" ./deploy.sh

# 변수 설정
IMAGE_NAME=${IMAGE_NAME:-"multinic-agent"}
IMAGE_TAG=${IMAGE_TAG:-"1.0.0"}
IMAGE_REPOSITORY=${IMAGE_REPOSITORY:-""}
IMAGE_PULL_POLICY=${IMAGE_PULL_POLICY:-"IfNotPresent"}
NAMESPACE=${NAMESPACE:-"multinic-system"}
RELEASE_NAME=${RELEASE_NAME:-"multinic-agent"}
SSH_PASSWORD=${SSH_PASSWORD:-"YOUR_SSH_PASSWORD"}
SSH_KEY_PATH=${SSH_KEY_PATH:-""}  # SSH Key 경로 (설정시 Key 인증 사용)
SSH_USER=${SSH_USER:-"root"}
CURRENT_NODE=${CURRENT_NODE:-"$(hostname)"}
DEBUG=${DEBUG:-"false"}

# 배포 모드 (tar | registry)
DEPLOY_MODE=${DEPLOY_MODE:-"tar"}
REGISTRY_HOST=${REGISTRY_HOST:-""}           # 예: nexus.local:5000
REGISTRY_USERNAME=${REGISTRY_USERNAME:-""}   # 필요 시
REGISTRY_PASSWORD=${REGISTRY_PASSWORD:-""}   # 필요 시
SKIP_TAR_LOAD=${SKIP_TAR_LOAD:-"false"}      # registry 모드에서 tar 로드 생략 가능
DISTRIBUTE_TAR=${DISTRIBUTE_TAR:-"true"}     # tar 모드에서 원격 노드 배포 여부

# ===== 여기에 추가 (라인 26부터) =====
# TAR 파일 경로 설정 (Build Skip용)
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
IMAGES_DIR="$SCRIPT_DIR/../deployments/images"

if [ "$DEBUG" = "true" ]; then
    echo "디버그 - SCRIPT_DIR: $SCRIPT_DIR"
    echo "디버그 - IMAGES_DIR: $IMAGES_DIR"
    ls -la "$IMAGES_DIR"
fi

TAR_FILE=${TAR_FILE:-""}
if [ -z "$TAR_FILE" ]; then
    TAR_FILE=$(find "$IMAGES_DIR" -maxdepth 1 -name "multinic-agent-${IMAGE_TAG}.tar" | head -1)
fi

# 이미지 저장소 결정
if [ "$DEPLOY_MODE" != "tar" ] && [ "$DEPLOY_MODE" != "registry" ]; then
    echo -e "${RED}✗ DEPLOY_MODE는 tar 또는 registry만 지원합니다${NC}"
    exit 1
fi
if [ -z "$IMAGE_REPOSITORY" ]; then
    if [ "$DEPLOY_MODE" = "registry" ]; then
        if [ -z "$REGISTRY_HOST" ]; then
            echo -e "${RED}✗ registry 모드에서는 REGISTRY_HOST가 필요합니다${NC}"
            exit 1
        fi
        IMAGE_REPOSITORY="${REGISTRY_HOST}/${IMAGE_NAME}"
    else
        IMAGE_REPOSITORY="${IMAGE_NAME}"
    fi
fi
# ===== 추가 끝 =====


# 모든 노드 목록을 동적으로 가져오기
ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))

echo -e "배포 모드: ${BLUE}${DEPLOY_MODE}${NC}"
echo -e "이미지: ${BLUE}${IMAGE_REPOSITORY}:${IMAGE_TAG}${NC}"
echo -e "네임스페이스: ${BLUE}${NAMESPACE}${NC}"
echo -e "클러스터 노드: ${BLUE}${ALL_NODES[*]}${NC}"

# SSH 인증 방식 확인
if [ -n "$SSH_KEY_PATH" ]; then
    echo -e "SSH 인증: ${BLUE}Key 인증 ($SSH_KEY_PATH)${NC}"
    SSH_OPTIONS="-i $SSH_KEY_PATH -o StrictHostKeyChecking=no"
    SCP_OPTIONS="-i $SSH_KEY_PATH -o StrictHostKeyChecking=no"
else
    echo -e "SSH 인증: ${BLUE}패스워드 인증${NC}"
    SSH_OPTIONS="-o StrictHostKeyChecking=no"
    SCP_OPTIONS="-o StrictHostKeyChecking=no"
fi

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
commands=("helm" "kubectl")
if [ "$DEPLOY_MODE" = "tar" ] || [ "$SKIP_TAR_LOAD" != "true" ]; then
    commands+=("nerdctl" "tar")
fi
if [ "$DEPLOY_MODE" = "tar" ] && [ "$DISTRIBUTE_TAR" = "true" ] && [ -z "$SSH_KEY_PATH" ]; then
    commands+=("sshpass")
fi
for cmd in "${commands[@]}"; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}✗ $cmd가 설치되어 있지 않습니다${NC}"
        exit 1
    fi
done
echo -e "${GREEN}✓ 모든 필수 도구 확인 완료${NC}"

# 3. 이미지 준비
echo -e "\n${BLUE}3. 이미지 준비${NC}"
if [ "$DEPLOY_MODE" = "registry" ] && [ "$SKIP_TAR_LOAD" = "true" ]; then
    if ! nerdctl images | awk '{print $1":"$2}' | grep -q "^${IMAGE_NAME}:${IMAGE_TAG}$"; then
        echo -e "${RED}✗ 로컬 이미지가 없습니다: ${IMAGE_NAME}:${IMAGE_TAG}${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ 로컬 이미지 확인 완료${NC}"
else
    if [ -z "$TAR_FILE" ] || [ ! -f "$TAR_FILE" ]; then
        echo -e "${RED}✗ TAR 파일 없음: $IMAGES_DIR 에 multinic-agent-${IMAGE_TAG}.tar 필요${NC}"
        exit 1
    fi
    echo -e "${YELLOW}TAR 파일 확인: $TAR_FILE${NC}"
    ls -lh "$TAR_FILE"

    # 파일 무결성 검사
    if [ ! -s "$TAR_FILE" ]; then
        echo -e "${RED}✗ TAR 파일이 비어 있습니다 (0 bytes)${NC}"
        exit 1
    fi
    if ! tar -tf "$TAR_FILE" >/dev/null 2>&1; then
        echo -e "${RED}✗ TAR 파일이 손상되었습니다 (tar 검증 실패)${NC}"
        exit 1
    fi
    echo -e "${GREEN}✓ TAR 파일 무결성 확인 완료${NC}"

    # nerdctl load (verbose + 에러 상세)
    if nerdctl load -i "$TAR_FILE" 2>&1 | tee /tmp/nerdctl-load.log; then
        echo -e "${GREEN}✓ TAR 이미지 로드 완료${NC}"
        echo "로드된 이미지:"
        nerdctl images | grep "$IMAGE_NAME" || true
    else
        echo -e "${RED}✗ TAR 이미지 로드 실패. 로그: /tmp/nerdctl-load.log${NC}"
        cat /tmp/nerdctl-load.log
        exit 1
    fi
fi

# 4. 이미지 배포 (tar | registry)
echo -e "\n${BLUE}4. 이미지 배포${NC}"
if [ "$DEPLOY_MODE" = "registry" ]; then
    TARGET_IMAGE="${IMAGE_REPOSITORY}:${IMAGE_TAG}"
    echo -e "${YELLOW}Registry: ${TARGET_IMAGE}${NC}"
    if [ -n "$REGISTRY_USERNAME" ] && [ -n "$REGISTRY_PASSWORD" ]; then
        echo -e "${YELLOW}Registry 로그인 중...${NC}"
        echo "$REGISTRY_PASSWORD" | nerdctl login --username "$REGISTRY_USERNAME" --password-stdin "$REGISTRY_HOST"
    fi
    nerdctl tag "${IMAGE_NAME}:${IMAGE_TAG}" "$TARGET_IMAGE"
    nerdctl push "$TARGET_IMAGE"
    echo -e "${GREEN}✓ Registry 푸시 완료${NC}"
else
    if [ "$DISTRIBUTE_TAR" != "true" ]; then
        echo -e "${YELLOW}원격 노드 배포 생략 (DISTRIBUTE_TAR=false)${NC}"
    else
        TMP_IMAGE_FILE="/tmp/$(basename "$TAR_FILE")"
        for node in "${ALL_NODES[@]}"; do
            if [ "$node" = "$CURRENT_NODE" ]; then
                echo -e "${GREEN}✓ ${node}: 현재 노드 (이미 로드됨)${NC}"
                continue
            fi

            echo -e "${YELLOW}노드 ${node}에 TAR 배포 중...${NC}"
            if [ -n "$SSH_KEY_PATH" ]; then
                if scp $SCP_OPTIONS "$TAR_FILE" "${SSH_USER}@${node}":/tmp/; then
                    if ssh $SSH_OPTIONS "${SSH_USER}@${node}" "nerdctl load -i ${TMP_IMAGE_FILE} && rm ${TMP_IMAGE_FILE}"; then
                        echo -e "${GREEN}✓ ${node}: TAR 로드 완료${NC}"
                    else
                        echo -e "${RED}✗ ${node}: TAR 로드 실패${NC}"
                        exit 1
                    fi
                else
                    echo -e "${RED}✗ ${node}: TAR 전송 실패${NC}"
                    exit 1
                fi
            else
                if sshpass -p "$SSH_PASSWORD" scp $SCP_OPTIONS "$TAR_FILE" "${SSH_USER}@${node}":/tmp/; then
                    if sshpass -p "$SSH_PASSWORD" ssh $SSH_OPTIONS "${SSH_USER}@${node}" "nerdctl load -i ${TMP_IMAGE_FILE} && rm ${TMP_IMAGE_FILE}"; then
                        echo -e "${GREEN}✓ ${node}: TAR 로드 완료${NC}"
                    else
                        echo -e "${RED}✗ ${node}: TAR 로드 실패${NC}"
                        exit 1
                    fi
                else
                    echo -e "${RED}✗ ${node}: TAR 전송 실패${NC}"
                    exit 1
                fi
            fi
        done
    fi
fi

# 5. CRD 배포
echo -e "\n${BLUE}5. CRD 배포${NC}"
CRD_FILE="deployments/crds/multinicnodeconfig-crd.yaml"

if [ -f "$CRD_FILE" ]; then
    echo -e "${YELLOW}CRD 적용 중...${NC}"

    # 기존 CRD가 있는지 확인
    if kubectl get crd multinicnodeconfigs.multinic.io >/dev/null 2>&1; then
        echo -e "${YELLOW}기존 CRD 발견 - 업데이트 모드${NC}"

        # 기존 CRD 삭제 후 새로 생성 (스키마 변경을 위해)
        echo -e "${YELLOW}기존 CRD 삭제 중...${NC}"
        kubectl delete crd multinicnodeconfigs.multinic.io --ignore-not-found=true

        echo -e "${YELLOW}CRD 삭제 완료, 5초 대기 중...${NC}"
        sleep 5
    fi

    # 새 CRD 적용
    if kubectl apply -f "$CRD_FILE"; then
        echo -e "${GREEN}✓ CRD 배포 완료${NC}"

        # CRD가 완전히 적용될 때까지 대기
        echo -e "${YELLOW}CRD 적용 확인 중...${NC}"
        sleep 5

        # CRD 상태 확인
        if kubectl get crd multinicnodeconfigs.multinic.io >/dev/null 2>&1; then
            echo -e "${GREEN}✓ CRD 정상 배포 확인${NC}"

            # interfaceStatuses 필드 타입 확인
            echo -e "${YELLOW}CRD 스키마 검증 중...${NC}"
            SCHEMA_TYPE=$(kubectl get crd multinicnodeconfigs.multinic.io -o jsonpath='{.spec.versions[0].schema.openAPIV3Schema.properties.status.properties.interfaceStatuses.type}' 2>/dev/null)
            if [ "$SCHEMA_TYPE" = "array" ]; then
                echo -e "${GREEN}✓ interfaceStatuses 스키마 확인: array 타입 (리스트 구조)${NC}"
            else
                echo -e "${YELLOW}⚠ interfaceStatuses 스키마: $SCHEMA_TYPE (예상: array)${NC}"
            fi
        else
            echo -e "${RED}✗ CRD 배포 확인 실패${NC}"
            exit 1
        fi
    else
        echo -e "${RED}✗ CRD 배포 실패${NC}"
        exit 1
    fi
else
    echo -e "${RED}✗ CRD 파일을 찾을 수 없습니다: $CRD_FILE${NC}"
    exit 1
fi

# 6. Helm 차트 배포 (이제 CRD Hook 불필요)
echo -e "\n${BLUE}6. Helm 차트 배포${NC}"
if helm upgrade --install $RELEASE_NAME ./deployments/helm \
    --namespace $NAMESPACE \
    --set image.repository=${IMAGE_REPOSITORY} \
    --set image.tag=${IMAGE_TAG} \
    --set image.pullPolicy=${IMAGE_PULL_POLICY} \
    --wait --timeout=300s; then
    echo -e "${GREEN}✓ Helm 차트 배포 완료${NC}"
else
    echo -e "${RED}✗ Helm 차트 배포 실패${NC}"
    exit 1
fi

# 7. 배포 확인
echo -e "\n${BLUE}7. 배포 상태 확인${NC}"
sleep 5

echo -e "\n${YELLOW}Controller 상태:${NC}"
kubectl get pods -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent-controller

echo -e "\n${YELLOW}MultiNIC NodeConfig:${NC}"
kubectl get multinicnodeconfigs.multinic.io -n $NAMESPACE

echo -e "\n${GREEN}✅ 배포 완료! MultiNIC Agent v1.0.0이 성공적으로 배포되었습니다.${NC}"
echo -e "\n${BLUE}로그 확인:${NC}"
echo -e "kubectl logs -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent-controller -f"
