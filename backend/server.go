package main

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

//go:embed frontend/dist
var frontendFS embed.FS

// 配置（环境变量）
var (
	version      = "dev" // 由构建时 -ldflags "-X main.version=..." 注入
	logDir       = getenv("LOG_DIR", "/var/log/tlog")
	logFile      = getenv("LOG_FILE", "tlog-session.log")
	dbPath       = getenv("DB_PATH", "/data/tlog.db")
	statePath    = getenv("STATE_PATH", "/data/collector.state")
	recDir       = getenv("REC_DIR", "/data/recordings") // 回放文件根目录(不分桶 <rec>.cast, 可配)
	httpAddr     = getenv("HTTP_ADDR", "0.0.0.0:8080")
	retentionDays = atoi(getenv("RETENTION_DAYS", "7"), 7)
	speedMax     = parseFloat(getenv("SPEED_MAX", "8"), 8)
)

func getenv(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func atoi(s string, def int) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func parseFloat(s string, def float64) float64 {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return f
}

var upgrader = websocket.Upgrader{
	CheckOrigin: func(r *http.Request) bool { return true }, // 由 OpenResty 前置鉴权，这里放行
	// 子协议声明：客户端以 ['Bearer', b64url(token)] 发起，gorilla 选中并回显 'Bearer'，
	// 满足浏览器对 Sec-WebSocket-Protocol 回显的要求；token 由后端从请求头解析。
	Subprotocols: []string{"Bearer"},
}

func main() {
	if err := openDB(dbPath); err != nil {
		log.Fatalf("openDB: %v", err)
	}

	// 安全启动校验: 拒绝弱密码/缺失 JWT 密钥, 避免审计系统裸奔
	if authKey == "" || authKey == "changeme" {
		log.Fatalf("安全错误: TLOG_KEY 仍为默认/空, 请设置强密码后再启动 (修改 .env 的 TLOG_KEY)")
	}
	if authSecret == "" {
		log.Fatalf("安全错误: TLOG_SECRET 未设置, 请设置独立随机长字符串 (修改 .env 的 TLOG_SECRET)")
	}

	// 回放目录可写性校验: 不可写会静默导致会话无回放文件(审计丢失), 启动即暴露
	if err := os.MkdirAll(recDir, 0o755); err != nil {
		log.Fatalf("回放目录创建失败 %s: %v", recDir, err)
	}
	if f, err := os.CreateTemp(recDir, ".writetest-"); err != nil {
		log.Fatalf("回放目录不可写 %s: %v", recDir, err)
	} else {
		_ = f.Close()
		_ = os.Remove(f.Name())
	}

	// 不分桶改造迁移: 把旧日期桶 .cast 合并到根目录并更新索引 file_path(幂等, 无丢失)
	if n, err := migrateLegacyBuckets(recDir); err != nil {
		log.Printf("migrate: %v", err)
	} else if n > 0 {
		log.Printf("migrate: merged %d legacy recordings to flat layout", n)
	}

	fullLogPath := filepath.Join(logDir, logFile)
	col := newCollector(fullLogPath, statePath, recDir)
	go col.Run()
	defer col.Stop()

	// 启动即清理一次(防膨胀, 不必等首个 hourly ticker); 再启动周期循环
	if n, err := purgeExpiredSessions(retentionDays, recDir, col.activeRecs()); err != nil {
		log.Printf("retention: startup purge: %v", err)
	} else if n > 0 {
		log.Printf("retention: startup purged %d expired sessions", n)
	}
	if m, e := scanOrphanRecordings(recDir); e == nil && m > 0 {
		log.Printf("retention: startup removed %d orphan recordings", m)
	}
	go retentionLoop(col)

	mux := http.NewServeMux()

	// 静态前端（embed ../frontend/dist 或 frontend/dist，FS 内带 "frontend/dist" 前缀）
	fe, _ := fs.Sub(frontendFS, "frontend/dist")
	fileServer := http.FileServer(http.FS(fe))
	// SPA fallback：找不到的真实文件回退到 index.html（保证 Vue 路由/刷新可用）
	spaHandler := func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if path == "index.html" {
			// 直接读取，避免 http.FileServer 对 embed.FS 根路径 "/" 的重定向问题
			serveIndex(w, fe)
			return
		}
		if fileExists(fe, path) {
			fileServer.ServeHTTP(w, r)
			return
		}
		// 非资源路径（如 /some/spa/route）回退 index.html
		if !strings.Contains(path, ".") {
			serveIndex(w, fe)
			return
		}
		http.NotFound(w, r)
	}
	mux.HandleFunc("/healthz", handleHealthz)
	mux.HandleFunc("/api/login", handleLogin) // 公开：登录签发 token
	mux.HandleFunc("/api/logout", requireAuth(handleLogout)) // 登出吊销 token
	mux.HandleFunc("/api/sessions", requireAuth(handleSessions))
	mux.HandleFunc("/api/users", requireAuth(handleUsers))
	mux.HandleFunc("/api/ws/play/", handleWSPlay) // 内部从 ?token= 校验
	mux.HandleFunc("/", spaHandler)

	srv := &http.Server{Addr: httpAddr, Handler: mux}

	// 优雅退出: 捕获 SIGTERM/SIGINT, 先停 collector(刷盘回放缓冲)再关 HTTP, 避免 kill 导致缓冲丢失
	sigCtx, stopSig := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stopSig()
	go func() {
		<-sigCtx.Done()
		log.Printf("tlog-web: received shutdown signal, flushing collector...")
		col.Stop()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
	}()

	log.Printf("tlog-web (go) listening on %s | log=%s db=%s rec_dir=%s retention=%dd", httpAddr, fullLogPath, dbPath, recDir, retentionDays)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		log.Fatalf("ListenAndServe: %v", err)
	}
}

func serveIndex(w http.ResponseWriter, fe fs.FS) {
	b, err := fs.ReadFile(fe, "index.html")
	if err != nil {
		http.Error(w, "index not found", 500)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(b)
}

func handleHealthz(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if !dbHealthy() {
		w.WriteHeader(503)
		json.NewEncoder(w).Encode(map[string]string{"status": "error", "detail": "db unavailable"})
		return
	}
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// sessionQuery 列表查询参数
type sessionQuery struct {
	Page     int
	PageSize int
	User     string
	Q        string
	DateFrom string // YYYY-MM-DD
	DateTo   string // YYYY-MM-DD
}

func parseSessionQuery(r *http.Request) sessionQuery {
	q := r.URL.Query()
	page := atoi(q.Get("page"), 1)
	if page < 1 {
		page = 1
	}
	pageSize := atoi(q.Get("page_size"), 50)
	if pageSize < 1 || pageSize > 500 {
		pageSize = 50
	}
	return sessionQuery{
		Page:     page,
		PageSize: pageSize,
		User:     strings.TrimSpace(q.Get("user")),
		Q:        strings.TrimSpace(q.Get("q")),
		DateFrom: strings.TrimSpace(q.Get("date_from")),
		DateTo:   strings.TrimSpace(q.Get("date_to")),
	}
}

func handleSessions(w http.ResponseWriter, r *http.Request) {
	q := parseSessionQuery(r)
	total, err := countSessions(q)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	rows, err := querySessions(q)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	out := make([]map[string]interface{}, 0, len(rows))
	for _, s := range rows {
		t := time.Unix(s.Time, 0).Format("2006-01-02 15:04:05")
		out = append(out, map[string]interface{}{
			"rec":     s.Rec,
			"user":    s.User,
			"time":    t,
			"summary": s.Summary,
		})
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Total-Count", strconv.FormatInt(total, 10))
	json.NewEncoder(w).Encode(map[string]interface{}{
		"total":     total,
		"page":      q.Page,
		"page_size": q.PageSize,
		"items":     out,
	})
}

// handleUsers 返回去重后的用户列表（供前端筛选下拉）
func handleUsers(w http.ResponseWriter, r *http.Request) {
	users, err := listUsers()
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(users)
}

func fileExists(fe fs.FS, name string) bool {
	if _, err := fs.Stat(fe, name); err != nil {
		return false
	}
	return true
}

func handleWSPlay(w http.ResponseWriter, r *http.Request) {
	// 从 Sec-WebSocket-Protocol 子协议取 token（不在 URL，避免泄露到日志/历史）
	tok := extractWSToken(r)
	if tok == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	if _, err := parseToken(tok); err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	rec := strings.TrimPrefix(r.URL.Path, "/api/ws/play/")
	rec = strings.Split(rec, "?")[0]
	rec, err := url.PathUnescape(rec)
	if err != nil {
		http.Error(w, "bad rec", 400)
		return
	}
	if rec == "" {
		http.Error(w, "bad rec", 400)
		return
	}
	sp := r.URL.Query().Get("speed")
	speed := parseFloat(sp, 1)
	if speed < 0.1 {
		speed = 0.1
	}
	if speed > speedMax {
		speed = speedMax
	}

	s, ok := findSession(rec)
	if !ok {
		http.Error(w, "rec not found: "+rec, 404)
		return
	}

	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close()

	done := make(chan struct{})

	// 心跳 + 读超时：检测死连接，避免消费 goroutine 泄漏。
	// 客户端 pong 时刷新读截止时间；ping 间隔由 ticker 驱动。
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	pingTicker := time.NewTicker(30 * time.Second)
	defer pingTicker.Stop()
	go func() {
		for {
			select {
			case <-pingTicker.C:
				// 写 ping 受写截止保护；发送失败说明连接已断
				conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
				if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
					return
				}
			case <-done:
				return
			}
		}
	}()

	go func() {
		// 消费客户端消息（避免控制帧积压），不需要处理；
		// 每次读前刷新截止时间，连接空闲/死掉时 ReadMessage 超时返回 → 清理。
		for {
			conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			if _, _, e := conn.ReadMessage(); e != nil {
				close(done)
				return
			}
		}
	}()

	onData := func(b []byte) {
		_ = conn.WriteMessage(websocket.BinaryMessage, b)
	}
	onClose := func() {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
	}
	// 同步回放（sleep 控制节奏），从 SQLite 读取；done 关闭或硬超时（1h）后退出，避免 goroutine 泄漏
	replayDone := make(chan struct{})
	go func() {
		_ = PlaySession(s, speed, onData, onClose)
		close(replayDone)
	}()
	select {
	case <-done:
	case <-replayDone:
	case <-time.After(1 * time.Hour): // 兜底：极端情况下强制退出
	}
}

func retentionLoop(col *collector) {
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for range ticker.C {
		n, err := purgeExpiredSessions(retentionDays, recDir, col.activeRecs())
		if err != nil {
			log.Printf("retention: purge sessions: %v", err)
		} else if n > 0 {
			log.Printf("retention: purged %d sessions (index + recording files) older than %d days", n, retentionDays)
		}
		// 孤儿兜底扫描(低频)
		if m, e := scanOrphanRecordings(recDir); e == nil && m > 0 {
			log.Printf("retention: removed %d orphan recordings", m)
		}
		purgeOldLogFiles(logDir, retentionDays)
	}
}

// purgeOldLogFiles 删除轮转后的旧日志文件（不删当前 tlog-session.log）。
func purgeOldLogFiles(dir string, days int) {
	cutoff := time.Now().AddDate(0, 0, -days)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// 只删轮转产物：.1 / .2.gz / .1.2026... 等，绝不删当前文件
		if name == logFile || strings.HasPrefix(name, logFile+".") == false {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			p := filepath.Join(dir, name)
			if err := os.Remove(p); err == nil {
				log.Printf("retention: removed old log %s", p)
			}
		}
	}
}

