package main

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

// timingSeg 是解析后的单段：延迟 delayUs 微秒后输出 nBytes 字节（取自 out_txt）。
type timingSeg struct {
	delayUs int64
	nBytes  int64
}

// parseTiming 解析 tlog TIMING 串，返回有序的 (延迟, 字节数) 段。
// 本机 tlog 实际存在两种格式（已实证 500+ 样本）：
//   标准格式（主流，约占 95%）：']m/n' 产出 m 字节（分 n 段），其后的 '>N'/'+N' 仅表示延迟微秒。
//     例：']1/1>2+167>5' → 先输出 1 字节(延迟累积)，再延迟 167μs，再延迟 5μs。
//   变体格式（少数）：无 ']'，直接用 '>N' 表示"延迟 N 微秒且输出 N 字节"。
//     例：'>100' → 输出 100 字节、延迟 100μs。
// 两种格式通过"是否含 ']' "区分；'='/'x' 为无延迟/倍率标记，跳过。
func parseTiming(timing string) []timingSeg {
	var segs []timingSeg
	pend := int64(0)
	hasBracket := strings.Contains(timing, "]")
	if hasBracket {
		// 标准格式：']m/n' 产出 m 字节（延迟使用此前累积的 pend），'>N'/'+N' 仅延迟
		i := 0
		for i < len(timing) {
			c := timing[i]
			switch c {
			case '>':
				v, ni := readNum(timing, i+1)
				pend += v
				i = ni
			case '+':
				v, ni := readNum(timing, i+1)
				pend += v
				i = ni
			case ']':
				m, ni := readNum(timing, i+1)
				i = ni
				if i < len(timing) && timing[i] == '/' {
					ni2 := i + 1
					for ni2 < len(timing) && timing[ni2] >= '0' && timing[ni2] <= '9' {
						ni2++
					}
					i = ni2
				}
				segs = append(segs, timingSeg{delayUs: pend, nBytes: m})
				pend = 0
			case '=', 'x':
				j := i + 1
				for j < len(timing) && (timing[j] == 'x' || (timing[j] >= '0' && timing[j] <= '9')) {
					j++
				}
				i = j
			default:
				i++
			}
		}
		return segs
	}

	// 变体格式（无 ']'）：'>N' 表示"延迟 N 微秒 + 输出 N 字节"。
	// 但存在固定延迟标记（如 '>100' 对应 out_len=20~33），此时 sum(>N) != out_len，
	// 退化整段输出，延迟取各段之和。
	var raw []int64 // 每段延迟
	i := 0
	for i < len(timing) {
		c := timing[i]
		switch c {
		case '>':
			v, ni := readNum(timing, i+1)
			pend += v
			raw = append(raw, pend)
			pend = 0
			i = ni
		case '+':
			v, ni := readNum(timing, i+1)
			pend += v
			i = ni
		case '=', 'x':
			j := i + 1
			for j < len(timing) && (timing[j] == 'x' || (timing[j] >= '0' && timing[j] <= '9')) {
				j++
			}
			i = j
		default:
			i++
		}
	}
	sumN := int64(0)
	for _, d := range raw {
		sumN += d
	}
	if sumN == int64(0) {
		return segs
	}
	return buildVariantSegs(raw)
}

// buildVariantSegs 生成变体格式段：每段延迟 d 对应输出 d 字节。
// 若各段延迟之和与 out_txt 实际长度不符（如固定延迟标记 '>100'），
// 由 emitRow 在校验时退化为整段输出。
func buildVariantSegs(delays []int64) []timingSeg {
	var segs []timingSeg
	for _, d := range delays {
		segs = append(segs, timingSeg{delayUs: d, nBytes: d})
	}
	return segs
}

// emitRow 按 timing 段推流一行 out_txt。
func emitRow(out, timing string, speed float64, onData func([]byte)) {
	segs := parseTiming(timing)
	pos := int64(0)
	totalBytes := int64(len(out))
	sumSeg := int64(0)
	for _, s := range segs {
		sumSeg += s.nBytes
	}
	if sumSeg != totalBytes && len(segs) > 0 {
		// timing 解析出的字节数与 out_txt 不符（变体格式退化解），整段输出，延迟取各段总和
		delay := int64(0)
		for _, s := range segs {
			delay += s.delayUs
		}
		segs = []timingSeg{{delayUs: delay, nBytes: totalBytes}}
	}
	for _, seg := range segs {
		if seg.delayUs > 0 {
			d := float64(seg.delayUs) / float64(speed) * 1000.0
			if d < 0 {
				d = 0
			}
			time.Sleep(time.Duration(d))
		}
		if seg.nBytes > 0 {
			chunk := outSlice(out, pos, seg.nBytes)
			pos += seg.nBytes
			if len(chunk) > 0 {
				onData([]byte(chunk))
			}
		}
	}
	if len(segs) == 0 && out != "" {
		// 无 timing 数据：按 speed 给基础帧间隔，避免瞬间喷完且保证速度设置生效
		baseMs := 20.0 / speed
		if baseMs > 0 {
			time.Sleep(time.Duration(baseMs * 1e6)) // 毫秒 -> 纳秒
		}
		onData([]byte(out))
	}
}

func readNum(s string, i int) (int64, int) {
	j := i
	for j < len(s) && s[j] >= '0' && s[j] <= '9' {
		j++
	}
	if j == i {
		return 0, i
	}
	v, _ := strconv.ParseInt(s[i:j], 10, 64)
	return v, j
}

// PlaySession 从物理回放文件(不分桶的 <rec>.cast)流式读取该 rec 的录制行并重演终端输出。
// 不再依赖 SQLite 存储回放流。s 为已通过 findSession 取到的索引行(含 file_path);
// onData 收到原始字节块, onClose 在结束时回调。speed 为倍速(>0)。
// 流式逐行读取+即时播放(避免大文件全载入内存 OOM), 按 seq 去重防同进程内重复 ingest。
func PlaySession(s SessionRow, speed float64, onData func([]byte), onClose func()) error {
	defer onClose()
	if speed <= 0 {
		speed = 1
	}
	if s.FilePath == "" {
		onData([]byte("\r\n[无该会话回放文件]\r\n"))
		return nil
	}
	f, err := os.Open(s.FilePath)
	if err != nil {
		onData([]byte("\r\n[打开回放文件失败: " + err.Error() + "]\r\n"))
		return err
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024) // 支持长行(单行可达数 MB)

	seen := make(map[int64]struct{})
	for scanner.Scan() {
		line := scanner.Text()
		// 行格式: "<seq>	<out>	<timing>	<inTxt>"
		parts := strings.SplitN(line, "	", 4)
		if len(parts) < 2 {
			continue
		}
		seq, _ := strconv.ParseInt(parts[0], 10, 64)
		if _, dup := seen[seq]; dup {
			continue // 重复 seq 跳过(防同进程重读翻倍)
		}
		seen[seq] = struct{}{}
		out := strings.ReplaceAll(parts[1], "\\n", "\n")
		out = strings.ReplaceAll(out, "\\r", "\r")
		timing := ""
		if len(parts) >= 3 {
			timing = parts[2]
		}
		emitRow(out, timing, speed, onData) // 即时播放, 不缓存全文件
	}
	if err := scanner.Err(); err != nil {
		onData([]byte("\r\n[读取回放失败: " + err.Error() + "]\r\n"))
		return err
	}
	return nil
}

func outSlice(s string, pos, n int64) string {
	if pos >= int64(len(s)) {
		return ""
	}
	end := pos + n
	if end > int64(len(s)) {
		end = int64(len(s))
	}
	return s[pos:end]
}
