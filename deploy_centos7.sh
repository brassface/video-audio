#!/usr/bin/env bash
set -euo pipefail

# 一键部署到 CentOS7（假设源码已在当前目录：main.go/go.mod/video-audio.service）
# 用法：
#   chmod +x deploy_centos7.sh
#   ./deploy_centos7.sh

APP_DIR="/opt/video-audio"
SERVICE_NAME="video-audio"
UNIT_SRC="./video-audio.service"
UNIT_DST="/etc/systemd/system/${SERVICE_NAME}.service"

echo "[1/6] 创建目录并复制文件到 ${APP_DIR}"
sudo mkdir -p "${APP_DIR}"
sudo cp -f ./main.go ./go.mod ./README.md "${APP_DIR}/"
sudo cp -f "${UNIT_SRC}" "${APP_DIR}/"

echo "[2/6] 检查/安装 ffmpeg"
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ffmpeg 未安装，尝试 yum 安装（如失败请改用你自己的安装方式/静态 ffmpeg）"
  sudo yum install -y ffmpeg || true
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "yum 未安装到 ffmpeg，尝试安装静态版 ffmpeg（仅支持 x86_64 常见云主机架构）"
  arch="$(uname -m || true)"
  if [[ "${arch}" != "x86_64" ]]; then
    echo "ERROR: 当前架构为 ${arch}，本脚本的静态 ffmpeg 自动安装仅支持 x86_64。"
    echo "请你手动安装适配架构的 ffmpeg，并确保命令 ffmpeg 可用，然后重跑本脚本。"
    exit 1
  fi

  cd /tmp
  curl -L -o ffmpeg-static.tar.xz "https://johnvansickle.com/ffmpeg/releases/ffmpeg-release-amd64-static.tar.xz"
  tar -xf ffmpeg-static.tar.xz
  dir="$(find . -maxdepth 1 -type d -name 'ffmpeg-*-amd64-static' | head -n 1)"
  if [[ -z "${dir}" ]]; then
    echo "ERROR: 解压静态 ffmpeg 失败。"
    exit 1
  fi
  sudo cp -f "${dir}/ffmpeg" /usr/local/bin/ffmpeg
  sudo cp -f "${dir}/ffprobe" /usr/local/bin/ffprobe
  sudo chmod +x /usr/local/bin/ffmpeg /usr/local/bin/ffprobe
fi
if ! command -v ffmpeg >/dev/null 2>&1; then
  echo "ERROR: ffmpeg 仍不可用。请检查 /usr/local/bin/ffmpeg 是否存在，以及 PATH 是否包含 /usr/local/bin。"
  exit 1
fi
echo "ffmpeg: $(command -v ffmpeg)"

echo "[3/6] 检查/安装 Go（仅编译用）"
if ! command -v go >/dev/null 2>&1; then
  echo "go 未安装，先尝试 yum 安装 golang（版本可能较旧但足够编译本项目）"
  sudo yum install -y golang || true

  if ! command -v go >/dev/null 2>&1; then
    echo "yum 未安装到 go，开始下载并安装 Go 1.20.14（一次性），带重试与镜像兜底"
    cd /tmp
    tarName="go1.20.14.linux-amd64.tar.gz"

    # 依次尝试多个下载源（部分网络环境 go.dev 不稳定）
    urls=(
      "https://golang.google.cn/dl/${tarName}"
      "https://dl.google.com/go/${tarName}"
      "https://go.dev/dl/${tarName}"
    )

    rm -f "${tarName}"
    for u in "${urls[@]}"; do
      echo "尝试下载: ${u}"
      if curl -L --retry 5 --retry-all-errors --connect-timeout 15 --max-time 1200 -o "${tarName}" "${u}"; then
        break
      fi
    done

    if [[ ! -s "${tarName}" ]]; then
      echo "ERROR: Go 下载失败。你可以手动下载 ${tarName} 到服务器 /tmp 后重跑脚本。"
      exit 1
    fi

    sudo rm -rf /usr/local/go
    sudo tar -C /usr/local -xzf "${tarName}"
    echo 'export PATH=$PATH:/usr/local/go/bin' | sudo tee /etc/profile.d/go.sh >/dev/null
    # shellcheck disable=SC1091
    source /etc/profile.d/go.sh
  fi
fi
go version

echo "[4/6] 编译二进制"
cd "${APP_DIR}"
sudo /usr/local/go/bin/go build -o video-audio . 2>/dev/null || sudo go build -o video-audio .
sudo chmod +x "${APP_DIR}/video-audio"

echo "[5/6] 安装并启动 systemd 服务"
sudo cp -f "${APP_DIR}/video-audio.service" "${UNIT_DST}"
sudo systemctl daemon-reload
sudo systemctl enable --now "${SERVICE_NAME}"
sudo systemctl status "${SERVICE_NAME}" --no-pager

echo "[6/6] 放行防火墙端口（如 firewalld 存在）"
if command -v firewall-cmd >/dev/null 2>&1; then
  sudo firewall-cmd --add-port=3003/tcp --permanent || true
  sudo firewall-cmd --reload || true
  sudo firewall-cmd --list-ports || true
fi

echo "部署完成。服务器本机自测："
echo "  curl -v --max-time 3 http://127.0.0.1:3003/health"


