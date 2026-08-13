package main

import (
	"bufio"
	"compress/gzip"
	"encoding/json"
	"io"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/fsnotify/fsnotify"
)

// fileState 是单个被跟踪文件的 checkpoint（对齐 Filebeat/Vector 的 file identity + offset）。
type fileState struct {
	Name   string `json:"name"`   // 文件名（用于重建路径）
	Offset int64  `json:"offset"` // 已读到的字节偏移
	Size   int64  `json:"size"`   // 上次 stat 的文件大小
	Done   bool   `json:"done"`   // 轮转件读完标记，避免重复全扫
}

// collector 跟踪匹配 logFile* 的所有文件（当前活跃 + 轮转件 + .gz 压缩件），
// 每个文件一个 harvester goroutine，按各自 checkpoint 增量续读。
// 对齐成熟采集器（Filebeat/Vector）的做法：file identity + checkpoint + glob 匹配轮转件 + truncate 自愈。
type collector struct {
	logFile   string // 基础名，如 tlog-session.log（glob 模式: logFile*）
	logDir    string
	statePath string
	recDir    string // 回放文件根目录(按日期分桶写入)

	mu     sync.Mutex
	seqCtr int64                 // 全局回放行序号，持久化以保证跨重启/跨文件唯一
	files  map[string]*fileState // key = fingerprint（inode 字符串），持久化 checkpoint
	running map[string]bool      // key = fingerprint，当前是否有 harvester 在跑（不持久化，与 checkpoint 分离）
	recFiles map[string]*recWriter // key = rec，当前打开的回放文件 writer 缓存
	wg      sync.WaitGroup         // 跟踪活跃 harvest goroutine, Stop 时等待全部退出再 flush

	stop      chan struct{}
	stopOnce sync.Once
}

// recWriter 包装一个按 rec 打开的回放文件(不分桶, REC_DIR/<rec>.cast), 带 bufio 缓冲.
type recWriter struct {
	f *os.File
	w *bufio.Writer
}

func newCollector(logPath, statePath, recDir string) *collector {
	return &collector{
		logFile:   filepath.Base(logPath),
		logDir:    filepath.Dir(logPath),
		statePath: statePath,
		recDir:    recDir,
		files:     make(map[string]*fileState),
		running:   make(map[string]bool),
		recFiles:  make(map[string]*recWriter),
		stop:      make(chan struct{}),
	}
}

// fingerprint 取文件 inode 作为身份标识（同机同设备稳定；预留扩展为 checksum）。
func (c *collector) fingerprint(path string) string {
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return strconv.FormatUint(uint64(st.Ino), 10)
	}
	return ""
}

// loadState 从磁盘恢复 checkpoint。
func (c *collector) loadState() {
	b, err := os.ReadFile(c.statePath)
	if err != nil {
		return
	}
	var st struct {
		SeqCtr int64                 `json:"seqCtr"`
		Files  map[string]*fileState `json:"files"`
	}
	if json.Unmarshal(b, &st) == nil {
		c.seqCtr = st.SeqCtr
		if st.Files != nil {
			c.files = st.Files
		}
	}
}

func (c *collector) saveState() {
	c.mu.Lock()
	defer c.mu.Unlock()
	st := struct {
		SeqCtr int64                 `json:"seqCtr"`
		Files  map[string]*fileState `json:"files"`
	}{c.seqCtr, c.files}
	b, _ := json.Marshal(st)
	_ = os.WriteFile(c.statePath, b, 0644)
}

// Run 启动多文件 harvester 跟踪循环。
func (c *collector) Run() {
	c.loadState()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Printf("collector: fsnotify: %v", err)
		return
	}
	defer watcher.Close()
	_ = watcher.Add(c.logDir)

	// 初次：对 glob 命中的所有文件启动 harvester（含当前 + 轮转件 + .gz）
	c.discover()

	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-c.stop:
			c.saveState()
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			// 目录出现/变更任何匹配 logFile 前缀的文件 → 重新发现（捕获新轮转件）
			if strings.HasPrefix(filepath.Base(ev.Name), c.logFile) {
				c.discover()
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return
			}
			log.Printf("collector: watch error: %v", err)
		case <-ticker.C:
			c.tick()
		}
	}
}

// discover 扫描 glob 匹配的所有文件，对未跟踪的启动 harvester。
func (c *collector) discover() {
	matches, err := filepath.Glob(filepath.Join(c.logDir, c.logFile+"*"))
	if err != nil {
		return
	}
	for _, path := range matches {
		fp := c.fingerprint(path)
		if fp == "" {
			continue
		}
		c.mu.Lock()
		_, running := c.running[fp]
		done := false
		if fs, ok := c.files[fp]; ok {
			done = fs.Done
		}
		c.mu.Unlock()
		if running {
			continue // 已有 harvester 在跑，不重复启动
		}
		if done {
			continue // 已读完的轮转件（Done），tick 勿重复拉起
		}
		// 若已有 checkpoint（state 加载或之前扫过），从 checkpoint 续读；否则从 0
		startOffset := int64(0)
		c.mu.Lock()
		if fs, ok := c.files[fp]; ok {
			startOffset = fs.Offset
		}
		c.running[fp] = true
		c.mu.Unlock()
		c.wg.Add(1)
		go c.harvest(path, fp, startOffset)
	}
}

// stopHarvest 标记某 fp 的 harvester 已退出（用于 discover 后续可重新启动）。
func (c *collector) markStopped(fp string) {
	c.mu.Lock()
	delete(c.running, fp)
	c.mu.Unlock()
}

// tick 周期任务：truncate 自愈 + 重新发现 + 持久化。
func (c *collector) tick() {
	c.discover() // 捕获新轮转件
	c.mu.Lock()
	for fp, fs := range c.files {
		if fs.Done {
			continue
		}
		path := filepath.Join(c.logDir, fs.Name)
		if fi, e := os.Stat(path); e == nil {
			if fs.Offset > fi.Size() {
				// copytruncate：inode 不变但文件被截断重写，offset 超界 → 归零重读
				log.Printf("collector: %s offset(%d)>size(%d), truncated, resync from 0",
					fs.Name, fs.Offset, fi.Size())
				fs.Offset = 0
			}
			fs.Size = fi.Size()
		}
		_ = fp
	}
	c.mu.Unlock()
	c.saveState()
}

// harvest 单个文件的读取循环（harvester）。
// 轮转件（非当前活跃文件，或 .gz 压缩件）读完即标记 Done 退出；
// 当前活跃文件常驻 tail（EOF 后短睡重试）。
// .gz 压缩件用 gzip.Reader 顺序解压读取（不可 seek，读完即 Done）。
func (c *collector) harvest(path, fp string, startOffset int64) {
	defer c.wg.Done()
	isGz := strings.HasSuffix(path, ".gz")
	isActive := filepath.Base(path) == c.logFile && !isGz

	c.mu.Lock()
	if _, ok := c.files[fp]; !ok {
		// 仅在无 checkpoint 时初始化（避免覆盖已持久化的 offset/Size/Done）
		c.files[fp] = &fileState{Name: filepath.Base(path), Offset: startOffset, Size: 0}
	}
	c.mu.Unlock()

	if isGz {
		c.harvestGz(path, fp)
		return
	}

	f, err := os.Open(path)
	if err != nil {
		log.Printf("collector: open %s: %v", path, err)
		return
	}
	defer f.Close()

	reader := bufio.NewReader(f)
	if startOffset > 0 {
		if _, e := f.Seek(startOffset, 0); e != nil {
			// Seek 失败（如文件被截断/权限变更）：归零重读，避免从错误偏移漏数据
			log.Printf("collector: seek %s @%d failed: %v, resync from 0", path, startOffset, e)
			c.mu.Lock()
			if c.files[fp] != nil {
				c.files[fp].Offset = 0
			}
			c.mu.Unlock()
			startOffset = 0
			if _, e2 := f.Seek(0, 0); e2 != nil {
				log.Printf("collector: reseek %s from 0: %v", path, e2)
				return
			}
		}
	}

	for {
		select {
		case <-c.stop:
			c.markStopped(fp)
			return
		default:
		}

		progressed := c.drainOnce(f, reader, fp)
		if progressed {
			continue
		}

		if !isActive {
			c.mu.Lock()
			if c.files[fp] != nil {
				c.files[fp].Done = true
			}
			c.mu.Unlock()
			c.markStopped(fp)
			return
		}

		// 当前活跃文件：EOF 后重试。必须重新定位到当前 offset 并新建 reader，
		// 否则 bufio.Reader 会缓存 EOF 状态，后续 append 的新内容永远读不到。
		c.mu.Lock()
		cur := c.files[fp].Offset
		c.mu.Unlock()
		f.Seek(cur, 0)
		reader = bufio.NewReader(f)
		time.Sleep(200 * time.Millisecond)
	}
}

// harvestGz 处理 .gz 轮转件：gzip 解压顺序读完（不可 seek），读完标记 Done。
func (c *collector) harvestGz(path, fp string) {
	gf, err := os.Open(path)
	if err != nil {
		log.Printf("collector: open %s: %v", path, err)
		return
	}
	defer gf.Close()
	gz, err := gzip.NewReader(gf)
	if err != nil {
		log.Printf("collector: gzip %s: %v", path, err)
		return
	}
	defer gz.Close()

	reader := bufio.NewReader(gz)
	var line []byte
	for {
		chunk, err := reader.ReadBytes('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if err == nil {
				c.ingest(string(line))
				c.mu.Lock()
				if c.files[fp] != nil {
					c.files[fp].Offset += int64(len(chunk))
				}
				c.mu.Unlock()
				line = line[:0]
			}
		}
		if err != nil {
			if err == io.EOF && len(line) > 0 {
				c.ingest(string(line))
				c.mu.Lock()
				if c.files[fp] != nil {
					c.files[fp].Offset += int64(len(line))
				}
				c.mu.Unlock()
			}
			break
		}
	}
	c.mu.Lock()
	if c.files[fp] != nil {
		c.files[fp].Done = true
	}
	c.mu.Unlock()
	log.Printf("collector: scanned rotated gzip %s", filepath.Base(path))
}

// drainOnce 从 reader 读取尽可能多的完整行（保留未完成的超长行到下次），
// 解析并 ingest，更新对应 fileState.Offset。返回是否有新行被处理。
func (c *collector) drainOnce(f *os.File, reader *bufio.Reader, fp string) bool {
	progressed := false
	line := make([]byte, 0) // 每次调用新建：跨 ReadBytes 累积超长行，但不跨调用污染
	for {
		chunk, err := reader.ReadBytes('\n')
		if len(chunk) > 0 {
			line = append(line, chunk...)
			if err == nil {
				// 完整一行（含 \n）
				c.ingest(string(line))
				line = line[:0]
				progressed = true
				// 更新 offset
				c.mu.Lock()
				if c.files[fp] != nil {
					c.files[fp].Offset += int64(len(chunk))
				}
				c.mu.Unlock()
			}
			// err==ErrBufferFull（超长行无 \n）时继续累积；err==io.EOF 时下方处理残行
		}
		if err != nil {
			// EOF：若已累积到未完成的行（无 \n），说明读到文件尾部。
			// 直接 ingest 该行并前进 offset，保证后续新 append 的内容能从正确位置续读，
			// 不遗漏新会话。代价是最后一行可能不完整（正在写入的会话），可接受。
			if len(line) > 0 {
				c.ingest(string(line))
				c.mu.Lock()
				if c.files[fp] != nil {
					c.files[fp].Offset += int64(len(line))
				}
				c.mu.Unlock()
				progressed = true
			}
			break
		}
	}
	return progressed
}

// ingest 处理一行已解析的完整日志行：回放流追加写入物理文件(按日期分桶) + 首次出现写 sessions 索引。
// 全局 seq 保证回放行顺序, 跨重启/跨文件不重复。
func (c *collector) ingest(raw string) {
	rec, user, tsec, out, timing, inTxt, summary, ok := parseLine(raw)
	if !ok {
		return
	}
	c.mu.Lock()
	seq := c.seqCtr
	c.seqCtr++
	c.mu.Unlock()

	now := time.Now().Unix()
	// 先写 DB 索引(保证 rec 进入 alive 集合), 再写物理回放文件。
	// 避免 scanOrphanRecordings 在"文件已建但 DB 行未插"的窗口误删该 .cast(TOCTOU 竞态)。
	if err := insertSession(SessionRow{
		Rec:       rec,
		User:      user,
		Time:      tsec,
		Summary:   summary,
		CreatedAt: now,
		FilePath:  c.recFilePath(rec),
	}); err != nil {
		log.Printf("collector: insertSession: %v", err)
	}
	// 回放流写物理文件(不分日期桶, 统一 REC_DIR/<rec>.cast)
	if err := c.appendRecording(rec, seq, out, timing, inTxt); err != nil {
		log.Printf("collector: appendRecording %s: %v", rec, err)
	}
}

// activeRecs 返回当前正在被 writer 持有(活跃写入中)的 rec 集合。
// 清理逻辑需跳过这些 rec, 避免删除正在审计的活跃会话文件(审计零丢失)。
func (c *collector) activeRecs() map[string]bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := make(map[string]bool, len(c.recFiles))
	for rec := range c.recFiles {
		m[rec] = true
	}
	return m
}

// recFilePath 返回某 rec 的回放文件路径。
// 不分日期桶: 统一 REC_DIR/<rec>.cast, 一个会话一个文件, 彻底避免跨午夜/跨采集延迟时
// 回放文件写入错位(db file_path 与物理文件不一致)导致的内容丢失。清理按 DB 索引的
// file_path 删除, 自洽。
func (c *collector) recFilePath(rec string) string {
	return filepath.Join(c.recDir, rec+".cast")
}

// appendRecording 把一行回放数据追加到 rec 对应的物理文件(不分日期桶, 统一 REC_DIR/<rec>.cast, 缓存 writer)。
// 行格式: "<seq>	<out>	<timing>	<inTxt>\n", PlaySession 逐行解析按 seq 重放。
// 因 writer 以 rec 为 key 缓存, 同一 rec 的不同文件(轮转/重启后重读)始终写入同一物理文件, 不会错位。
// 关键: 本进程首次打开某 rec 的文件用 O_TRUNC(而非 O_APPEND)。容器重启/logrotate 改 inode 导致
// 旧 checkpoint 失效、从 0 重读旧日志时, 不会把重读内容追加到上次运行的文件末尾造成回放翻倍
// (旧内容被新重读内容截断重写)。续写仍靠 recFiles 缓存的 writer; 跨重启写入乱序由 PlaySession 按 seq 排序兜底。
func (c *collector) appendRecording(rec string, seq int64, out, timing, inTxt string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	rw, ok := c.recFiles[rec]
	if !ok {
		if err := os.MkdirAll(c.recDir, 0o755); err != nil {
			return err
		}
		// O_TRUNC: 首次打开即截断旧内容(来自上次运行), 避免重启重读翻倍; 新文件 O_CREATE 等价
		f, err := os.OpenFile(c.recFilePath(rec), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
		if err != nil {
			return err
		}
		rw = &recWriter{f: f, w: bufio.NewWriter(f)}
		c.recFiles[rec] = rw
	}
	// 转义换行, 避免单行被截断(回放按行读)
	line := strings.ReplaceAll(out, "\n", "\\n")
	line = strings.ReplaceAll(line, "\r", "\\r")
	_, err := rw.w.WriteString(strconv.FormatInt(seq, 10) + "	" + line + "	" + timing + "	" + inTxt + "\n")
	if err != nil {
		return err
	}
	// 每条回放行写入后立即 flush 到磁盘(page cache), 保证回放文件实时可见、不依赖攒够 200 行或进程退出。
	// 审计正确性优先于极致写入吞吐; Flush 不 fsync, 开销可接受。
	if err := rw.w.Flush(); err != nil {
		return err
	}
	return nil
}

// flushAll 关闭所有 rec writer(Stop 时调用, 确保缓冲落盘)。
func (c *collector) flushAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for rec, rw := range c.recFiles {
		_ = rw.w.Flush()
		_ = rw.f.Close()
		delete(c.recFiles, rec)
	}
}

// Stop 停止跟踪, 等待所有 harvest goroutine 退出后再 flush 回放文件缓冲,
// 避免 flushAll 关闭 writer 后 harvest 仍在写入导致的数据丢失/句柄泄漏.
// 幂等: 信号 handler 与 defer 可能各调用一次, 用 stopOnce 保证只真正关闭一次.
func (c *collector) Stop() {
	c.stopOnce.Do(func() {
		close(c.stop)
		c.wg.Wait()
		c.flushAll()
	})
}

// parseLine 解析一行 tlog JSON，提取回放所需的字段。
// 返回 ok=false 表示该行不是合法录制行（噪声/半行/非 JSON）。
func parseLine(line string) (rec, user string, timeSec int64, out, timing, inTxt, summary string, ok bool) {
	line = strings.TrimSpace(line)
	line = strings.Trim(line, "\x00")
	if line == "" {
		return
	}
	var d struct {
		Rec    string  `json:"rec"`
		User   string  `json:"user"`
		Time   float64 `json:"time"`
		Out    string  `json:"out_txt"`
		Timing string  `json:"timing"`
		InTxt  string  `json:"in_txt"`
	}
	if err := json.Unmarshal([]byte(line), &d); err != nil {
		return
	}
	if d.Rec == "" {
		return
	}
	return d.Rec, d.User, int64(d.Time), d.Out, d.Timing, d.InTxt, cleanSummary(d.Out), true
}
