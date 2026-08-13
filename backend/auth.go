package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// loginFails 记录每个 IP 的失败时间戳（滑动窗口限速）
var loginFails sync.Map // ip -> []time.Time

// 认证配置（环境变量）
var (
	authUser   = getenv("TLOG_USER", "admin")
	authKey    = getenv("TLOG_KEY", "changeme")
	authSecret = getenv("TLOG_SECRET", "") // JWT 签名密钥；必须独立设置强随机串, 不允许复用 authKey
	tokenTTL   = 12 * time.Hour

	// 登录限速（内存滑动窗口，防暴力破解）
	loginMaxAttempts = atoi(getenv("LOGIN_MAX_ATTEMPTS", "5"), 5)
	loginLockWindow  = time.Duration(atoi(getenv("LOGIN_LOCK_WINDOW_MIN", "5"), 5)) * time.Minute

	// 令牌吊销（内存黑名单，key=jti，value=过期时间）。登出即失效。
	// 重启清空（token TTL 短，可接受）；如需跨重启吊销可改为持久化。
	revokedTokens sync.Map // jti(string) -> exp(int64)
)

// checkLoginAllowed 返回该 IP 是否允许尝试；若超限返回 false 并清掉过期记录
func checkLoginAllowed(ip string) bool {
	now := time.Now()
	v, ok := loginFails.Load(ip)
	if !ok {
		return true
	}
	times := v.([]time.Time)
	// 保留窗口内的失败记录
	cut := now.Add(-loginLockWindow)
	kept := times[:0]
	for _, t := range times {
		if t.After(cut) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= loginMaxAttempts {
		loginFails.Store(ip, kept)
		return false
	}
	loginFails.Store(ip, kept)
	return true
}

// recordLoginFail 记录一次失败
func recordLoginFail(ip string) {
	now := time.Now()
	v, _ := loginFails.LoadOrStore(ip, []time.Time{})
	times := append(v.([]time.Time), now)
	loginFails.Store(ip, times)
}

// clearLoginFails 登录成功后清除该 IP 失败记录
func clearLoginFails(ip string) {
	loginFails.Delete(ip)
}

// jwtClaim 是 token 载荷（HS256，自实现以避免额外依赖）
type jwtClaim struct {
	Sub string `json:"sub"` // 用户名
	Jti string `json:"jti"` // 唯一标识，用于吊销
	Exp int64  `json:"exp"` // 过期 unix 秒
	Iat int64  `json:"iat"` // 签发 unix 秒
	Nbf int64  `json:"nbf"` // 生效 unix 秒（not before）
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func b64urlDecode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(s)
}

// randHex 返回 n 字节的十六进制随机串（用于 jti）
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		// 极不可能失败；退化用时间避免崩溃
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}

// issueToken 签发 HS256 JWT
func issueToken(user string) (string, error) {
	now := time.Now()
	claim := jwtClaim{
		Sub: user,
		Jti: randHex(16),
		Iat: now.Unix(),
		Nbf: now.Unix(),
		Exp: now.Add(tokenTTL).Unix(),
	}
	payload, err := json.Marshal(claim)
	if err != nil {
		return "", err
	}
	header := b64url([]byte(`{"alg":"HS256","typ":"JWT"}`))
	body := b64url(payload)
	signing := header + "." + body
	mac := hmac.New(sha256.New, []byte(authSecret))
	mac.Write([]byte(signing))
	sig := b64url(mac.Sum(nil))
	return signing + "." + sig, nil
}

// parseToken 校验 HS256 JWT，返回 claim；无效返回 error
func parseToken(token string) (jwtClaim, error) {
	var empty jwtClaim
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return empty, os.ErrInvalid
	}
	signing := parts[0] + "." + parts[1]
	mac := hmac.New(sha256.New, []byte(authSecret))
	mac.Write([]byte(signing))
	expected := b64url(mac.Sum(nil))
	// 恒定时间比较，防时序攻击
	if !hmac.Equal([]byte(expected), []byte(parts[2])) {
		return empty, os.ErrPermission
	}
	payload, err := b64urlDecode(parts[1])
	if err != nil {
		return empty, err
	}
	var c jwtClaim
	if err := json.Unmarshal(payload, &c); err != nil {
		return empty, err
	}
	if time.Now().Unix() > c.Exp {
		return empty, os.ErrDeadlineExceeded
	}
	if time.Now().Unix() < c.Nbf {
		return empty, os.ErrDeadlineExceeded
	}
	// 吊销检查：登出后的 token 立即失效
	if _, revoked := revokedTokens.Load(c.Jti); revoked {
		return empty, os.ErrPermission
	}
	return c, nil
}

// revokeToken 将 token 加入黑名单（登出即失效，至 exp 到期自动清理）
func revokeToken(jti string, exp int64) {
	revokedTokens.Store(jti, exp)
}

// handleLogout 吊销当前 token（前端登出时调用，使 token 立即失效）
func handleLogout(w http.ResponseWriter, r *http.Request) {
	tok := extractToken(r)
	if tok == "" {
		http.Error(w, "unauthorized", 401)
		return
	}
	c, err := parseToken(tok)
	if err != nil {
		http.Error(w, "unauthorized", 401)
		return
	}
	revokeToken(c.Jti, c.Exp)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

// extractToken 从 Authorization 头取 token（HTTP 接口用）
func extractToken(r *http.Request) string {
	ah := r.Header.Get("Authorization")
	if strings.HasPrefix(ah, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(ah, "Bearer "))
	}
	return ""
}

// extractWSToken 从 WebSocket 的 Sec-WebSocket-Protocol 子协议头取 token。
// 前端以 ['Bearer', b64url(token)] 发起（token 含 '.' 非法，故 base64url 编码），
// 浏览器序列化该头为 "Bearer, <b64url(token)>"。这里取最后一项并 base64url 解码得 JWT。
func extractWSToken(r *http.Request) string {
	proto := r.Header.Get("Sec-WebSocket-Protocol")
	if proto == "" {
		return ""
	}
	parts := strings.Split(proto, ",")
	if len(parts) < 2 {
		return ""
	}
	enc := strings.TrimSpace(parts[len(parts)-1])
	// 兼容有/无 padding 的 base64url
	var dec []byte
	var err error
	if dec, err = base64.RawURLEncoding.DecodeString(enc); err != nil {
		if dec, err = base64.URLEncoding.DecodeString(enc); err != nil {
			// 非 base64url（旧客户端直传 token），原样返回
			return enc
		}
	}
	return string(dec)
}

// handleLogin 校验 user/key，签发 token
func handleLogin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	// 登录限速：超限直接 429
	clientIP := clientIP(r)
	if !checkLoginAllowed(clientIP) {
		http.Error(w, "too many attempts, try later", 429)
		return
	}
	var req struct {
		User string `json:"user"`
		Key  string `json:"key"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "bad request", 400)
		return
	}
	if req.User != authUser || req.Key != authKey {
		recordLoginFail(clientIP)
		log.Printf("auth: login failed for user=%q from %s", req.User, clientIP)
		http.Error(w, "invalid credentials", 401)
		return
	}
	clearLoginFails(clientIP)
	tok, err := issueToken(req.User)
	if err != nil {
		http.Error(w, "token issue failed", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"token": tok})
}

// clientIP 取真实客户端 IP（兼容 X-Forwarded-For / X-Real-IP，最后退到 RemoteAddr）
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// 取第一个（最原始客户端）
		parts := strings.Split(xff, ",")
		return strings.TrimSpace(parts[0])
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// requireAuth 包装受保护 handler，校验 token；失败返回 401
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tok := extractToken(r)
		if tok == "" {
			http.Error(w, "unauthorized", 401)
			return
		}
		if _, err := parseToken(tok); err != nil {
			http.Error(w, "unauthorized", 401)
			return
		}
		next(w, r)
	}
}
