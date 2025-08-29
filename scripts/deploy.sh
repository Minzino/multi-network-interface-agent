#!/bin/bash

set -e

# 색상 정의
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

echo -e "${GREEN}🚀 MultiNIC Agent v2 완전 자동 배포 스크립트${NC}"

# 변수 설정
IMAGE_NAME=${IMAGE_NAME:-"multinic-agent"}
IMAGE_TAG=${IMAGE_TAG:-"0.5.0"}
NAMESPACE=${NAMESPACE:-"default"}
RELEASE_NAME=${RELEASE_NAME:-"multinic-agent"}
SSH_PASSWORD=${SSH_PASSWORD:-"YOUR_SSH_PASSWORD"}

# 모든 노드 목록을 동적으로 가져오기
ALL_NODES=($(kubectl get nodes -o jsonpath='{.items[*].metadata.name}'))

echo -e "이미지: ${BLUE}${IMAGE_NAME}:${IMAGE_TAG}${NC}"
echo -e "네임스페이스: ${BLUE}${NAMESPACE}${NC}"
echo -e "릴리즈명: ${BLUE}${RELEASE_NAME}${NC}"
echo -e "클러스터 노드: ${BLUE}${ALL_NODES[*]}${NC}"

# 1. 네임스페이스 생성
echo -e "\n${BLUE}📁 1단계: 네임스페이스 생성${NC}"
if kubectl create namespace $NAMESPACE --dry-run=client -o yaml | kubectl apply -f -; then
    echo -e "${GREEN}✓ 네임스페이스 생성/확인 완료${NC}"
else
    echo -e "${RED}✗ 네임스페이스 생성 실패${NC}"
    exit 1
fi

# 2. BuildKit 설정 확인
echo -e "\n${BLUE}🔧 2단계: BuildKit 설정 확인${NC}"
if ! command -v buildkitd &> /dev/null; then
    echo -e "${YELLOW}BuildKit이 설치되어 있지 않습니다. 설치를 시작합니다...${NC}"
    
    # BuildKit 설치
    BUILDKIT_VERSION="v0.12.5"
    ARCH=$(uname -m)
    case $ARCH in
        x86_64) ARCH="amd64" ;;
        aarch64) ARCH="arm64" ;;
        armv7l) ARCH="armv7" ;;
        *) echo -e "${RED}지원하지 않는 아키텍처: $ARCH${NC}"; exit 1 ;;
    esac

    DOWNLOAD_URL="https://github.com/moby/buildkit/releases/download/${BUILDKIT_VERSION}/buildkit-${BUILDKIT_VERSION}.linux-${ARCH}.tar.gz"
    
    TMP_DIR=$(mktemp -d)
    cd $TMP_DIR
    
    echo -e "${YELLOW}BuildKit 다운로드 중...${NC}"
    curl -L -o buildkit.tar.gz "$DOWNLOAD_URL"
    tar -xzf buildkit.tar.gz
    sudo cp bin/* /usr/local/bin/
    
    cd - > /dev/null
    rm -rf $TMP_DIR
    
    echo -e "${GREEN}✓ BuildKit 설치 완료${NC}"
else
    echo -e "${GREEN}✓ BuildKit이 이미 설치되어 있습니다${NC}"
fi

# 3. containerd 확인 및 시작
echo -e "\n${BLUE}⚙️ 3단계: containerd 확인${NC}"
if ! systemctl is-active --quiet containerd; then
    echo -e "${YELLOW}containerd를 시작합니다...${NC}"
    sudo systemctl start containerd
    sleep 2
fi

if systemctl is-active --quiet containerd; then
    echo -e "${GREEN}✓ containerd 실행 중${NC}"
else
    echo -e "${RED}✗ containerd 시작 실패${NC}"
    exit 1
fi

# 4. buildkitd 확인 및 시작
echo -e "\n${BLUE}🛠️ 4단계: buildkitd 확인 및 시작${NC}"
if ! pgrep -f buildkitd > /dev/null; then
    echo -e "${YELLOW}buildkitd를 시작합니다...${NC}"
    
    # systemd 서비스 설정
    cat > /tmp/buildkitd.service << 'EOF'
[Unit]
Description=BuildKit daemon
After=containerd.service
Requires=containerd.service

[Service]
Type=notify
ExecStart=/usr/local/bin/buildkitd --containerd-worker=true --containerd-worker-namespace=k8s.io
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

    sudo cp /tmp/buildkitd.service /etc/systemd/system/
    sudo systemctl daemon-reload
    sudo systemctl enable buildkitd
    sudo systemctl start buildkitd
    
    # 연결 대기
    echo -e "${YELLOW}buildkitd 연결 대기 중...${NC}"
    for i in {1..15}; do
        if buildctl debug workers &>/dev/null; then
            echo -e "${GREEN}✓ buildkitd 연결 성공${NC}"
            break
        elif [ $i -eq 15 ]; then
            echo -e "${RED}✗ buildkitd 연결 실패${NC}"
            exit 1
        else
            echo "연결 시도 $i/15..."
            sleep 2
        fi
    done
else
    echo -e "${GREEN}✓ buildkitd가 이미 실행 중입니다${NC}"
fi

# 5. 필수 도구 확인
echo -e "\n${BLUE}🔍 5단계: 필수 도구 확인${NC}"
commands=("nerdctl" "helm" "kubectl")
for cmd in "${commands[@]}"; do
    if ! command -v $cmd &> /dev/null; then
        echo -e "${RED}✗ $cmd가 설치되어 있지 않습니다${NC}"
        exit 1
    fi
done

# sshpass 설치 확인
if ! command -v sshpass &> /dev/null; then
    echo -e "${YELLOW}sshpass가 설치되어 있지 않습니다. 설치를 시도합니다...${NC}"
    if command -v apt-get &> /dev/null; then
        sudo apt-get update && sudo apt-get install -y sshpass
    elif command -v yum &> /dev/null; then
        sudo yum install -y sshpass
    elif command -v dnf &> /dev/null; then
        sudo dnf install -y sshpass
    elif command -v zypper &> /dev/null; then
        sudo zypper install -y sshpass
    else
        echo -e "${RED}✗ sshpass 설치에 실패했습니다. 수동으로 설치해주세요${NC}"
        exit 1
    fi
    
    if command -v sshpass &> /dev/null; then
        echo -e "${GREEN}✓ sshpass 설치 완료${NC}"
    else
        echo -e "${RED}✗ sshpass 설치 실패${NC}"
        exit 1
    fi
fi

echo -e "${GREEN}✓ 필수 도구 확인 완료${NC}"

# 6. 이미지 빌드
echo -e "\n${BLUE}📦 6단계: 이미지 빌드${NC}"
cd "$(dirname "$0")/.."

echo -e "${YELLOW}nerdctl로 이미지 빌드 중...${NC}"
nerdctl --namespace=k8s.io --address /var/run/containerd/containerd.sock build --no-cache -t ${IMAGE_NAME}:${IMAGE_TAG} .

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 이미지 빌드 완료${NC}"
else
    echo -e "${RED}✗ 이미지 빌드 실패${NC}"
    exit 1
fi

# 7. 이미지를 tar로 저장
echo -e "\n${BLUE}💾 7단계: 이미지 저장${NC}"
echo -e "${YELLOW}이미지를 tar 파일로 저장 중...${NC}"
nerdctl --namespace=k8s.io --address /var/run/containerd/containerd.sock save ${IMAGE_NAME}:${IMAGE_TAG} -o ${IMAGE_NAME}-${IMAGE_TAG}.tar

if [ $? -eq 0 ]; then
    echo -e "${GREEN}✓ 이미지 저장 완료${NC}"
else
    echo -e "${RED}✗ 이미지 저장 실패${NC}"
    exit 1
fi

# 8. 모든 노드에 이미지 배포
echo -e "\n${BLUE}🚚 8단계: 모든 노드에 이미지 배포${NC}"
for node in "${ALL_NODES[@]}"; do
    echo -e "${YELLOW}📦 $node 노드에 이미지 전송 중...${NC}"
    
    if sshpass -p "$SSH_PASSWORD" scp -o StrictHostKeyChecking=no ${IMAGE_NAME}-${IMAGE_TAG}.tar $node:/tmp/ 2>/dev/null; then
        echo -e "${YELLOW}🔧 $node 노드에 이미지 로드 중...${NC}"
        
        # nerdctl만 사용하도록 고정
        echo -e "${BLUE}INFO: nerdctl을 사용하여 이미지 로드${NC}"
        
        # nerdctl로 이미지 로드 (sudo 제거 - 이미 root로 실행 중)
        LOAD_COMMAND="nerdctl --namespace=k8s.io load -i /tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar && rm /tmp/${IMAGE_NAME}-${IMAGE_TAG}.tar"
        if sshpass -p "$SSH_PASSWORD" ssh -o StrictHostKeyChecking=no $node "${LOAD_COMMAND}"; then
            echo -e "${GREEN}✓ $node 노드 완료${NC}"
        else
            echo -e "${YELLOW}⚠️  $node 노드 이미지 로드 실패 (계속 진행)${NC}"
        fi
    else
        echo -e "${YELLOW}⚠️  $node 노드 접근 실패 (계속 진행)${NC}"
    fi
done

# 로컬 tar 파일 정리
echo -e "${BLUE}🗑️ 로컬 tar 파일 정리...${NC}"
rm -f ${IMAGE_NAME}-${IMAGE_TAG}.tar
echo -e "${GREEN}✓ 모든 노드에 이미지 배포 완료${NC}"

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
    --set image.repository=$IMAGE_NAME \
    --set image.tag=$IMAGE_TAG \
    --set image.pullPolicy=IfNotPresent \
    ${HELM_EXTRA_ARGS} \
    --wait --timeout=5m; then

    echo -e "${GREEN}✓ MultiNIC Agent 배포 완료${NC}"
else
    echo -e "${RED}✗ MultiNIC Agent 배포 실패${NC}"
    exit 1
fi



# 12. 전체 상태 확인
echo -e "\n${BLUE}📊 12단계: 전체 시스템 상태 확인${NC}"
echo "=================================================="
echo "📋 Controller Deployment 상태:"
kubectl get deploy -n $NAMESPACE -l app.kubernetes.io/component=controller -o wide || true

echo ""
echo "📋 생성된 Agent Jobs:"
kubectl get jobs -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent -o wide || true

echo ""
echo "📋 Agent Job Pods 상태:"
kubectl get pods -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent -o wide || true

echo ""
echo "📋 컨트롤러 로그 (최근 50줄):"
CTRL_POD=$(kubectl get pods -n $NAMESPACE -l app.kubernetes.io/component=controller -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)
if [ -n "$CTRL_POD" ]; then
  kubectl logs -n $NAMESPACE $CTRL_POD --tail=50 || true
else
  echo "controller pod not found"
fi
echo "=================================================="

echo -e "\n${GREEN}🎉 MultiNIC Controller+Job 배포가 완료되었습니다!${NC}"

echo -e "\n${YELLOW}📖 사용법:${NC}"
echo -e "  • 컨트롤러 로그: ${BLUE}kubectl logs -n $NAMESPACE -l app.kubernetes.io/component=controller -f${NC}"
echo -e "  • 생성된 Job 확인: ${BLUE}kubectl get jobs -n $NAMESPACE -l app.kubernetes.io/name=multinic-agent${NC}"
echo -e "  • Agent Job 로그: ${BLUE}kubectl logs -n $NAMESPACE job/<job-name>${NC}"

echo -e "\n${BLUE}🔧 다음 단계:${NC}"
echo -e "  1. 데이터베이스에 테스트 데이터 추가"
echo -e "  2. 네트워크 인터페이스 생성 모니터링"
echo -e "  3. Agent 로그에서 처리 상황 확인"

echo -e "\n${BLUE}🗑️  삭제 방법:${NC}"
echo -e "  ${YELLOW}helm uninstall $RELEASE_NAME -n $NAMESPACE${NC}"

echo -e "\n${YELLOW}⚠️  참고사항:${NC}"
echo -e "  • Agent는 DaemonSet으로 모든 노드에서 실행됩니다"
echo -e "  • buildkitd는 systemd 서비스로 자동 시작됩니다"
echo -e "  • 네트워크 설정 변경을 위해 privileged 권한이 필요합니다"
echo -e "  • 실패한 설정은 자동으로 롤백됩니다"
