package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAddr        = ":3003"
	defaultMaxUploadMB = 2048 // 2GB
	defaultTimeoutSec  = 1800 // 30min
)

func main() {
	addr := getenv("ADDR", defaultAddr)
	maxUploadMB := getenvInt("MAX_UPLOAD_MB", defaultMaxUploadMB)
	timeoutSec := getenvInt("FFMPEG_TIMEOUT_SEC", defaultTimeoutSec)
	tempDir := getenv("TMP_DIR", os.TempDir())
	ffmpegPath := getenv("FFMPEG_PATH", "ffmpeg")
	// MP3 输出参数（可通过环境变量调整，降低体积）
	mp3Bitrate := getenv("MP3_BITRATE", "96k")       // 例如 64k/96k/128k/192k
	mp3Channels := getenvInt("MP3_CHANNELS", 2)      // 2=双声道（强制要求）
	mp3SampleRate := getenvInt("MP3_SAMPLE_RATE", 44100) // 例如 22050/32000/44100
	defaultVideoPath := getenv("DEFAULT_VIDEO_PATH", "/opt/video-audio/default.mp4")

	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})

	mux.HandleFunc("/extract-audio", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		maxBytes := int64(maxUploadMB) * 1024 * 1024
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)

		// 支持两种模式：
		// 1) multipart/form-data 上传 file
		// 2) 不传 file：使用服务器本地默认视频 DEFAULT_VIDEO_PATH
		var (
			file     multipart.File
			filename string
			haveFile bool
			nameHint string
		)
		ct := r.Header.Get("Content-Type")
		if strings.HasPrefix(ct, "multipart/form-data") {
			if err := r.ParseMultipartForm(32 << 20); err != nil {
				http.Error(w, "parse multipart failed: "+err.Error(), http.StatusBadRequest)
				return
			}
			nameHint = strings.TrimSpace(r.FormValue("name"))
			f, fh, err := r.FormFile("file")
			if err == nil {
				file = f
				filename = fh.Filename
				haveFile = true
				defer file.Close()
			}
		} else {
			_ = r.ParseForm()
			nameHint = strings.TrimSpace(r.FormValue("name"))
		}
		if q := strings.TrimSpace(r.URL.Query().Get("name")); q != "" {
			nameHint = q
		}

		reqID := randID(12)
		inPath := ""
		outPath := filepath.Join(tempDir, fmt.Sprintf("audio_%s.mp3", reqID))

		// 确保无论如何都清理临时音频文件
		defer func() { _ = os.Remove(outPath) }()

		ctx, cancel := context.WithTimeout(r.Context(), time.Duration(timeoutSec)*time.Second)
		defer cancel()

		var downloadName string
		if haveFile {
			filenameSafe := safeBaseName(filename)
			inPath = filepath.Join(tempDir, fmt.Sprintf("upload_%s_%s", reqID, filenameSafe))
			defer func() { _ = os.Remove(inPath) }()
			if err := saveToFile(inPath, file); err != nil {
				status := http.StatusInternalServerError
				if errors.Is(err, http.ErrBodyReadAfterClose) {
					status = http.StatusBadRequest
				}
				http.Error(w, "save upload failed: "+err.Error(), status)
				return
			}
			if nameHint != "" {
				downloadName = outputFileNameFromInput(nameHint, ".mp3")
			} else {
				downloadName = outputFileNameFromInput(filename, ".mp3")
			}
		} else {
			// 不传文件：使用默认视频文件
			st, err := os.Stat(defaultVideoPath)
			if err != nil || st.IsDir() {
				http.Error(w, "default video not found on server", http.StatusBadRequest)
				return
			}
			inPath = defaultVideoPath
			if nameHint != "" {
				downloadName = outputFileNameFromInput(nameHint, ".mp3")
			} else {
				downloadName = outputFileNameFromInput(filepath.Base(defaultVideoPath), ".mp3")
			}
		}

		if err := extractAudioMp3(ctx, ffmpegPath, inPath, outPath, mp3Bitrate, mp3SampleRate, mp3Channels); err != nil {
			log.Printf("ffmpeg failed reqID=%s err=%v", reqID, err)
			http.Error(w, "ffmpeg extract failed: "+err.Error(), http.StatusInternalServerError)
			return
		}

		serveAudioFile(w, r, outPath, downloadName, mp3Bitrate, mp3SampleRate, mp3Channels)
	})

	srv := &http.Server{
		Addr:              addr,
		Handler:           logging(mux),
		ReadHeaderTimeout: 10 * time.Second,
	}

	log.Printf("listening on %s", addr)
	log.Fatal(srv.ListenAndServe())
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s from=%s ua=%q dur=%s", r.Method, r.URL.Path, r.RemoteAddr, r.UserAgent(), time.Since(start))
	})
}

func getUploadFile(r *http.Request) (multipart.File, string, error) {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "multipart/form-data") {
		return nil, "", fmt.Errorf("content-type must be multipart/form-data")
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		return nil, "", fmt.Errorf("parse multipart failed: %w", err)
	}
	f, fh, err := r.FormFile("file")
	if err != nil {
		return nil, "", fmt.Errorf(`missing form file field "file": %w`, err)
	}
	return f, fh.Filename, nil
}

func saveToFile(path string, src io.Reader) error {
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	_, err = io.Copy(out, src)
	return err
}

// 输出 mp3（需要 ffmpeg 支持 libmp3lame；静态 ffmpeg 一般都支持）
func extractAudioMp3(ctx context.Context, ffmpegPath, inputPath, outputPath, bitrate string, sampleRate, channels int) error {
	// -vn: 不要视频
	// -acodec libmp3lame: mp3 编码器
	// -b:a: 目标码率（越小体积越小）
	// -ar 44100: 采样率
	// -ac 2: 双声道
	args := []string{
		"-y",
		"-i", inputPath,
		"-vn",
		"-acodec", "libmp3lame",
		"-b:a", strings.TrimSpace(bitrate),
		"-ar", strconv.Itoa(sampleRate),
		"-ac", strconv.Itoa(channels),
		outputPath,
	}
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	// 把 stderr 合并出来便于定位 ffmpeg 报错
	out, err := cmd.CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}

func getenv(key, def string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	return v
}

func getenvInt(key string, def int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return def
	}
	i, err := strconv.Atoi(v)
	if err != nil || i <= 0 {
		return def
	}
	return i
}

func randID(nBytes int) string {
	b := make([]byte, nBytes)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func safeBaseName(name string) string {
	// 只保留文件名，避免路径穿越；并替换危险字符
	base := filepath.Base(name)
	base = strings.ReplaceAll(base, `"`, "_")
	base = strings.ReplaceAll(base, `'`, "_")
	base = strings.ReplaceAll(base, " ", "_")
	if base == "." || base == string(filepath.Separator) || base == "" {
		return "upload.bin"
	}
	return base
}

// 用输入文件名生成输出文件名：只替换扩展名（例如 a.mp4 -> a.mp3）
// 注意：这里尽量保留原始文件名（含中文/空格），下载时通过 Content-Disposition 的 filename* 支持。
func outputFileNameFromInput(inputFilename, newExt string) string {
	base := filepath.Base(inputFilename)
	// 防 header 注入
	base = strings.ReplaceAll(base, "\r", "")
	base = strings.ReplaceAll(base, "\n", "")
	nameNoExt := strings.TrimSuffix(base, filepath.Ext(base))
	if nameNoExt == "" || nameNoExt == "." {
		nameNoExt = "audio"
	}
	if !strings.HasPrefix(newExt, ".") {
		newExt = "." + newExt
	}
	return nameNoExt + newExt
}

// 同时设置 filename(安全兜底) + filename*(UTF-8 原名)，最大程度“使用输入的视频文件名”
func contentDispositionAttachment(filename string) string {
	// ASCII 兜底（避免引号/空格导致兼容问题）
	fallback := safeBaseName(filename)
	utf8Name := strings.ReplaceAll(filename, "\r", "")
	utf8Name = strings.ReplaceAll(utf8Name, "\n", "")
	// RFC 5987: filename*=UTF-8''<url-encoded-utf8>
	return fmt.Sprintf(`attachment; filename="%s"; filename*=UTF-8''%s`, fallback, url.PathEscape(utf8Name))
}

func serveAudioFile(w http.ResponseWriter, r *http.Request, path, downloadName, mp3Bitrate string, mp3SampleRate, mp3Channels int) {
	f, err := os.Open(path)
	if err != nil {
		http.Error(w, "open output failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		http.Error(w, "stat output failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "audio/mpeg")
	// 便于排错：用 curl -v 可以直接看到服务当前实际使用的音频参数
	w.Header().Set("X-Audio-Bitrate", mp3Bitrate)
	w.Header().Set("X-Audio-Sample-Rate", strconv.Itoa(mp3SampleRate))
	w.Header().Set("X-Audio-Channels", strconv.Itoa(mp3Channels))
	w.Header().Set("Content-Disposition", contentDispositionAttachment(downloadName))
	w.Header().Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	w.WriteHeader(http.StatusOK)

	if _, err := io.Copy(w, f); err != nil {
		log.Printf("write response interrupted err=%v", err)
		return
	}
}

