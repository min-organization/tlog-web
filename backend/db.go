package main

import (
	"database/sql"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

// SessionRow 是一个 SSH 录制会话的索引（会话列表用，一条 rec 一行）。
// 回放实体已移出 SQLite，存于 file_path 指向的物理文件。
type SessionRow struct {
	Rec       string
	User      string
	Time      int64 // unix 秒
	Summary   string
	CreatedAt int64 // unix 秒，用于保留策略
	FilePath  string // 回放文件绝对路径(不分桶: <REC_DIR>/<rec>.cast)
}

var (
	db   *sql.DB
	dbMu sync.Mutex
)

// openDB 打开（或创建）SQLite 库，建表，开启 WAL。
func openDB(path string) error {
	var err error
	db, err = sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	// WAL：读写并发互不阻塞（collector 写、HTTP 读）
	db.SetMaxOpenConns(4)
	if _, err = db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		return err
	}
	if _, err = db.Exec("PRAGMA synchronous=NORMAL"); err != nil {
		return err
	}
	schema := `
	CREATE TABLE IF NOT EXISTS sessions (
		rec TEXT PRIMARY KEY,
		user TEXT,
		time INTEGER,
		summary TEXT,
		created_at INTEGER,
		file_path TEXT
	);`
	if _, err = db.Exec(schema); err != nil {
		return fmt.Errorf("create table: %w", err)
	}
	if _, err = db.Exec("CREATE INDEX IF NOT EXISTS idx_sessions_time ON sessions(time DESC)"); err != nil {
		return err
	}
	return nil
}

// insertSession 写入会话索引。ON CONFLICT(rec) DO UPDATE：time 取更早者（MIN），
// 避免并发 recording 交错日志时首行时间偏晚；summary/created_at/file_path 保留首次出现值。
func insertSession(s SessionRow) error {
	dbMu.Lock()
	defer dbMu.Unlock()
	_, err := db.Exec(`
		INSERT INTO sessions(rec,user,time,summary,created_at,file_path) VALUES(?,?,?,?,?,?)
		ON CONFLICT(rec) DO UPDATE SET
			time = MIN(time, excluded.time)
	`, s.Rec, s.User, s.Time, s.Summary, s.CreatedAt, s.FilePath)
	return err
}

// findSession 检查某 rec 是否存在于索引（回放前存在性检查用）。
func findSession(rec string) (SessionRow, bool) {
	dbMu.Lock()
	defer dbMu.Unlock()
	row := db.QueryRow(
		`SELECT rec,user,time,summary,created_at,file_path FROM sessions WHERE rec=?`, rec,
	)
	var s SessionRow
	err := row.Scan(&s.Rec, &s.User, &s.Time, &s.Summary, &s.CreatedAt, &s.FilePath)
	if err == sql.ErrNoRows {
		return s, false
	}
	if err != nil {
		return s, false
	}
	return s, true
}

// purgeExpiredSessions 删除超过 retention 天的索引行, 并同步删除对应的回放物理文件。
// active 为当前正在写入的活跃会话 rec 集合, 这些会话即使"看似"过期也跳过(审计零丢失,
// 避免删除正在进行的会话; 其 created_at 通常很新也不会命中, 此处为双保险)。
// 先删文件再删行(崩溃最多留'DB有文件无'的死索引, 无害)。
// 注意: 先持锁 SELECT 出过期清单后立即释放锁, 再逐个删文件 + DELETE(每次单独短锁),
// 避免清理期间长时间持有全局 dbMu 阻塞所有 HTTP 查询。
func purgeExpiredSessions(retentionDays int, recDir string, active map[string]bool) (int64, error) {
	cutoff := time.Now().AddDate(0, 0, -retentionDays).Unix()
	dbMu.Lock()
	rows, err := db.Query("SELECT rec, file_path FROM sessions WHERE created_at < ?", cutoff)
	if err != nil {
		dbMu.Unlock()
		return 0, err
	}
	type rec2 struct {
		rec, path string
	}
	var expired []rec2
	for rows.Next() {
		var r rec2
		if err := rows.Scan(&r.rec, &r.path); err != nil {
			rows.Close()
			dbMu.Unlock()
			return 0, err
		}
		expired = append(expired, r)
	}
	rows.Close()
	dbMu.Unlock()

	var n int64
	for _, r := range expired {
		// 跳过正在进行的活跃会话(审计零丢失: 不删正在审计的会话文件)
		if active[r.rec] {
			continue
		}
		if r.path != "" {
			// 先删文件, 再删行(崩溃最多留'DB有文件无'的死索引, 无害)
			if err := os.Remove(r.path); err != nil && !os.IsNotExist(err) {
				log.Printf("retention: remove recording %s: %v", r.path, err)
			}
			// 清理可能已空的日期目录
			if dir := filepath.Dir(r.path); isDirEmpty(dir) {
				_ = os.Remove(dir)
			}
		}
		dbMu.Lock()
		if _, err := db.Exec("DELETE FROM sessions WHERE rec=?", r.rec); err == nil {
			n++
		}
		dbMu.Unlock()
	}
	return n, nil
}

// isDirEmpty 判断目录是否存在且为空
func isDirEmpty(dir string) bool {
	f, err := os.Open(dir)
	if err != nil {
		return false
	}
	defer f.Close()
	_, err = f.Readdirnames(1)
	return err == io.EOF
}

// scanOrphanRecordings 孤儿录制兜底扫描: 比对 DB 中存在的 rec 集合, 删除 REC_DIR 下
// 不在集合中的 .cast 文件(如 crash 中途留下的孤儿, 或不分桶改造前的残留日期桶目录)。
// 不分日期桶后 REC_DIR 下直接平铺 <rec>.cast, 此处直接扫描根目录。
// 注意: 仅删文件, 不删 DB 索引行(若文件丢失但索引在, 回放会明确报错而非静默丢失)。
// 返回删除的文件数。
func scanOrphanRecordings(recDir string) (int64, error) {
	dbMu.Lock()
	rows, err := db.Query("SELECT rec FROM sessions")
	if err != nil {
		dbMu.Unlock()
		return 0, err
	}
	alive := make(map[string]struct{})
	for rows.Next() {
		var rec string
		if err := rows.Scan(&rec); err == nil {
			alive[rec] = struct{}{}
		}
	}
	rows.Close()
	dbMu.Unlock()

	var removed int64
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return 0, nil // 目录不存在则无需扫描
	}
	for _, e := range entries {
		if e.IsDir() {
			// 不分桶改造前的残留日期桶目录: 若整体为空则移除, 其内部 .cast 由上层调用方处理;
			// 这里仅清理空目录, 避免误删仍在 DB 中的文件(日期桶内的 rec 也需在 alive 中)。
			if sub, e2 := os.ReadDir(filepath.Join(recDir, e.Name())); e2 == nil && len(sub) == 0 {
				_ = os.Remove(filepath.Join(recDir, e.Name()))
			}
			continue
		}
		if !strings.HasSuffix(e.Name(), ".cast") {
			continue
		}
		rec := strings.TrimSuffix(e.Name(), ".cast")
		if _, ok := alive[rec]; !ok {
			p := filepath.Join(recDir, e.Name())
			if err := os.Remove(p); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

// dbHealthy 探测数据库可用性（healthz 用）。
func dbHealthy() bool {
	dbMu.Lock()
	defer dbMu.Unlock()
	var n int
	return db.QueryRow("SELECT 1").Scan(&n) == nil
}

// escapeLike 转义 LIKE 通配符，使用户输入的 % _ [ 按字面匹配（避免 % 命中全表）。
// 转义符用反斜杠，配合查询中的 ESCAPE '\'。
func escapeLike(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`%`, `\%`,
		`_`, `\_`,
		`[`, `\[`,
	)
	return r.Replace(s)
}

// buildWhere 根据查询参数构造 WHERE 子句与参数（参数化，防注入）。
func buildWhere(q sessionQuery) (string, []interface{}) {
	var conds []string
	var args []interface{}
	if q.User != "" {
		conds = append(conds, "user LIKE ? ESCAPE '\\'")
		args = append(args, "%"+escapeLike(q.User)+"%")
	}
	if q.Q != "" {
		conds = append(conds, "(summary LIKE ? ESCAPE '\\' OR rec LIKE ? ESCAPE '\\')")
		args = append(args, "%"+escapeLike(q.Q)+"%", "%"+escapeLike(q.Q)+"%")
	}
	if q.DateFrom != "" {
		// 服务器本地时区对齐（sessions.time 为 unix 秒）
		conds = append(conds, "datetime(time,'unixepoch','localtime') >= ?")
		args = append(args, q.DateFrom+" 00:00:00")
	}
	if q.DateTo != "" {
		conds = append(conds, "datetime(time,'unixepoch','localtime') <= ?")
		args = append(args, q.DateTo+" 23:59:59")
	}
	if len(conds) == 0 {
		return "", args
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// querySessions 分页 + 过滤查询（后端分页）。
func querySessions(q sessionQuery) ([]SessionRow, error) {
	where, args := buildWhere(q)
	offset := (q.Page - 1) * q.PageSize
	sqlStr := `SELECT rec,user,time,summary,created_at FROM sessions` +
		where + ` ORDER BY time DESC, rec DESC LIMIT ? OFFSET ?`
	args = append(args, q.PageSize, offset)

	dbMu.Lock()
	defer dbMu.Unlock()
	rows, err := db.Query(sqlStr, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionRow
	for rows.Next() {
		var s SessionRow
		if err := rows.Scan(&s.Rec, &s.User, &s.Time, &s.Summary, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

// countSessions 返回符合条件的总行数（分页用）。
func countSessions(q sessionQuery) (int64, error) {
	where, args := buildWhere(q)
	sqlStr := `SELECT COUNT(*) FROM sessions` + where
	dbMu.Lock()
	defer dbMu.Unlock()
	var n int64
	if err := db.QueryRow(sqlStr, args...).Scan(&n); err != nil {
		return 0, err
	}
	return n, nil
}

// listUsers 返回去重后的用户列表。
func listUsers() ([]string, error) {
	dbMu.Lock()
	defer dbMu.Unlock()
	rows, err := db.Query(`SELECT DISTINCT user FROM sessions ORDER BY user`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var u string
		if err := rows.Scan(&u); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, nil
}

// migrateLegacyBuckets 不分桶改造的启动迁移: 把旧日期桶目录(YYYY-MM-DD/*.cast)
// 合并到 REC_DIR 根目录(<rec>.cast)。合并规则:
//   - 根目录无该 rec 文件 -> 直接移动
//   - 根目录已有(多为重启重读追加的重复内容) -> 源内容追加到目标后删源, 重复行由 PlaySession 按 seq 去重
//   - 同步 UPDATE sessions.file_path 指向根目录新路径
// 迁移幂等: 无日期桶目录时直接返回。保证旧数据回放路径与新代码一致, 无丢失。
func migrateLegacyBuckets(recDir string) (int64, error) {
	entries, err := os.ReadDir(recDir)
	if err != nil {
		return 0, nil
	}
	var moved int64
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// 仅处理 YYYY-MM-DD 形式的旧桶目录
		name := e.Name()
		if len(name) != 10 || name[4] != '-' || name[7] != '-' {
			continue
		}
		dateDir := filepath.Join(recDir, name)
		files, err := os.ReadDir(dateDir)
		if err != nil {
			continue
		}
		for _, f := range files {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".cast") {
				continue
			}
			rec := strings.TrimSuffix(f.Name(), ".cast")
			src := filepath.Join(dateDir, f.Name())
			dst := filepath.Join(recDir, f.Name())
			if _, err := os.Stat(dst); os.IsNotExist(err) {
				if err := os.Rename(src, dst); err != nil {
					log.Printf("migrate: move %s: %v", src, err)
					continue
				}
			} else {
				// 合并: 源追加入目标, 删源
				if err := appendFileInto(src, dst); err != nil {
					log.Printf("migrate: merge %s: %v", src, err)
					continue
				}
				_ = os.Remove(src)
			}
			// 更新索引 file_path 指向根目录
			dbMu.Lock()
			_, _ = db.Exec("UPDATE sessions SET file_path=? WHERE rec=?", dst, rec)
			dbMu.Unlock()
			moved++
		}
		// 删空日期桶目录
		if fs2, _ := os.ReadDir(dateDir); len(fs2) == 0 {
			_ = os.Remove(dateDir)
		}
	}
	return moved, nil
}

// appendFileInto 把 src 全部内容追加到 dst 末尾(用于迁移合并旧桶与根目录重复文件)。
func appendFileInto(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}
