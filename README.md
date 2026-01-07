## 视频转音频服务（CentOS7，端口 3003）

一个**极简** HTTP 服务：接收上传的视频文件，调用 `ffmpeg` 抽取音频并返回（默认 **MP3 / 双声道**）。

支持两种使用方式：

-   **带文件上传**：`curl -F "file=@xxx.mp4"` → 返回 `xxx.mp3`
-   **不带文件**：使用服务器上的**默认视频** `DEFAULT_VIDEO_PATH` → 返回 `default.mp3`

### 接口

-   **健康检查**：`GET /health` → `ok`
-   **抽取音频**：`POST /extract-audio`
    -   **上传模式**：`multipart/form-data`，字段名 `file`
    -   **默认视频模式**：不传 `file`，直接 POST 即可（使用 `DEFAULT_VIDEO_PATH`）
    -   响应：`audio/mpeg`，带 `Content-Disposition: attachment`（下载文件名为视频同名 `.mp3`）

### 快速部署（推荐：systemd）

#### 1) 准备依赖

-   **ffmpeg**：必须
-   **Go**：仅用于编译一次（编译后运行只需要二进制 + ffmpeg）

如果 yum 没有 ffmpeg：建议安装静态 ffmpeg 到 `/usr/local/bin/ffmpeg`，确保 `ffmpeg -version` 正常。

#### 2) 编译并安装服务

把 `main.go`、`go.mod`、`video-audio.service` 放到服务器 `/opt/video-audio`：

```bash
mkdir -p /opt/video-audio
cd /opt/video-audio
go build -o video-audio .
cp /opt/video-audio/video-audio.service /etc/systemd/system/video-audio.service
systemctl daemon-reload
systemctl enable --now video-audio
systemctl status video-audio --no-pager
```

> 注意：需要放行 **TCP 3003**（云安全组 + 服务器防火墙）。

### 使用示例（无需前端）

#### Windows：绕过本机代理

如果你看到 `Uses proxy env variable http_proxy ...`，说明 `curl` 走了代理，会影响访问。建议加上 `--noproxy "*"`。

#### 1) 健康检查

```bash
curl -v --noproxy "*" --max-time 5 http://SERVER_IP:3003/health
```

#### 2) 上传视频并保存音频（Windows CMD 示例）

```bat
curl -f --noproxy "*" -X POST "http://SERVER_IP:3003/extract-audio" -F "file=@D:\\path\\to\\video.mp4" -o "D:\\path\\to\\video.mp3"
```

#### 3) 不传文件：使用服务器默认视频

```bash
curl -f -X POST "http://SERVER_IP:3003/extract-audio" -o default.mp3
```

### 配置项（环境变量）

在 `video-audio.service` 里通过 `Environment=...` 配置：

-   **ADDR**：监听地址，默认 `:3003`
-   **MAX_UPLOAD_MB**：最大上传大小（MB），默认 `2048`
-   **FFMPEG_TIMEOUT_SEC**：ffmpeg 超时（秒），默认 `1800`
-   **TMP_DIR**：临时目录，默认系统临时目录（建议 `/tmp`）
-   **FFMPEG_PATH**：ffmpeg 路径，默认 `ffmpeg`
-   **MP3_BITRATE**：mp3 目标码率，示例 `128k/160k/192k/256k`
-   **MP3_CHANNELS**：声道数，默认 `2`（双声道）
-   **MP3_SAMPLE_RATE**：采样率，默认 `44100`
-   **DEFAULT_VIDEO_PATH**：服务器本地默认视频路径，默认 `/opt/video-audio/default.mp4`

> 体积粗略估算：MP3 大小约等于 码率(kbps) × 时长(秒) / 8。  
> 经验值：约 **1MB/分钟** ≈ **128kbps**，约 **2MB/分钟** ≈ **256kbps**

### 排错

先在服务器本机自测（最关键）：

```bash
curl -v --max-time 3 http://127.0.0.1:3003/health
```

-   如果本机也 **Connection refused**：服务没起来/没监听
    -   `systemctl status video-audio --no-pager`
    -   `journalctl -u video-audio -n 200 --no-pager`
    -   `ss -lntp | grep 3003 || true`
-   如果本机正常（返回 ok），但你电脑访问不通：通常是 **云安全组/防火墙** 未放行 3003
    -   `firewall-cmd --list-ports || true`
