package selfupdate

import (
	"archive/zip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"runtime"
	"strings"
	"time"

	"github.com/Autumn-27/artex/distribution"
)

// sumsAsset 是 release.yml 生成的校验和清单，覆盖 Release 里全部 zip。
const sumsAsset = "SHA256SUMS"

// maxBinarySize 限制解压出来的二进制体积，防止畸形 zip 把磁盘写满。
const maxBinarySize = 512 << 20 // 512 MiB

// maxZIPSize 是压缩发布包的下载预算；现有发布包约 15 MiB，保留扩展空间。
const maxZIPSize = 128 << 20 // 128 MiB

// Phase 是升级过程中的阶段，直接用作 SSE 事件里的 phase 字段。
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseDownload Phase = "downloading"
	PhaseVerify   Phase = "verifying"
	PhaseExtract  Phase = "extracting"
	PhaseStaged   Phase = "staged"
	PhaseFailed   Phase = "failed"
)

// Progress 由调用方提供，用来把进度推给前端。pct 仅在下载阶段有意义（0-100），
// 其余阶段传 -1。
type Progress func(ph Phase, pct int, msg string)

// Stage 下载指定 Release 的当前平台发布包，校验后把新二进制暂存为 artex.new。
//
// 走的是完整 zip 而不是裸二进制，理由有两个：现有 Release 的 SHA256SUMS 本来就
// 只覆盖 zip，走 zip 不需要改 CI，也能兼容已经发布出去的历史版本；zip 里还带着
// skills/，为将来同步内置 skill 留了口子。代价只是多下载 skills 那几百 KB。
//
// 函数返回即代表暂存完成，调用方随后优雅关闭并以 ExitRestart 退出。
func Stage(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress) error {
	if distribution.Managed() {
		return ErrManagedInstallation
	}
	p, err := ResolvePaths()
	if err != nil {
		return err
	}
	return stageAt(ctx, c, rel, currentVersion, prog, p, maxZIPSize)
}

func stageAt(ctx context.Context, c *http.Client, rel *Release, currentVersion string, prog Progress, p Paths, zipBudget int64) error {
	if prog == nil {
		prog = func(Phase, int, string) {}
	}
	if _, err := os.Lstat(p.New); err == nil {
		return fmt.Errorf("待安装路径 %s 已存在，请先重启完成升级；路径异常时请检查并处理暂存文件后重试", p.New)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查待安装的新版本: %w", err)
	}
	if err := checkWritable(p.Dir); err != nil {
		return err
	}

	name := AssetName(rel.TagName, runtime.GOOS, runtime.GOARCH)
	asset, ok := rel.FindAsset(name)
	if !ok {
		return fmt.Errorf("该版本没有提供 %s/%s 的发布包（缺少 %s）", runtime.GOOS, runtime.GOARCH, name)
	}
	if asset.Size > zipBudget {
		return fmt.Errorf("发布包声明大小 %s 超过下载上限 %s", humanSize(asset.Size), humanSize(zipBudget))
	}

	prog(PhaseDownload, 0, "获取校验和清单…")
	sums, err := fetchSums(ctx, c, rel)
	if err != nil {
		return err
	}
	want, ok := sums[name]
	if !ok {
		return fmt.Errorf("%s 未收录 %s，拒绝安装未经校验的二进制", sumsAsset, name)
	}

	// 同目录随机独占文件避免固定路径碰撞，并保留同文件系统 rename。
	zf, err := os.CreateTemp(p.Dir, ".artex-update-zip-*")
	if err != nil {
		return fmt.Errorf("创建下载临时文件: %w", err)
	}
	zipPath := zf.Name()
	defer func() {
		_ = zf.Close()
		_ = os.Remove(zipPath)
	}()

	prog(PhaseDownload, 0, fmt.Sprintf("下载 %s（%s）…", name, humanSize(asset.Size)))
	got, err := download(ctx, c, asset, zf, zipBudget, prog)
	if err != nil {
		return err
	}
	if err := zf.Close(); err != nil {
		return fmt.Errorf("关闭下载文件: %w", err)
	}

	prog(PhaseVerify, -1, "校验 SHA256…")
	if !strings.EqualFold(got, want) {
		return fmt.Errorf("SHA256 不匹配：期望 %s，实际 %s（下载损坏或被篡改）", short(want), short(got))
	}

	prog(PhaseExtract, -1, "解压并冒烟测试…")
	bf, err := os.CreateTemp(p.Dir, ".artex-update-bin-*.exe")
	if err != nil {
		return fmt.Errorf("创建二进制临时文件: %w", err)
	}
	binPath := bf.Name()
	defer func() {
		_ = bf.Close()
		_ = os.Remove(binPath)
	}()
	if err := extractBinary(zipPath, bf); err != nil {
		return err
	}
	if err := bf.Close(); err != nil {
		return fmt.Errorf("关闭新二进制: %w", err)
	}
	if err := smokeTest(binPath); err != nil {
		return fmt.Errorf("新版本无法在当前系统上运行: %w", err)
	}

	// 暂存件自己的 sha256 单独存一份：下次启动换装前还要再校验一次，
	// 防止暂存后到重启前这段时间里文件被改动或写坏。
	binSum, err := fileSHA256(binPath)
	if err != nil {
		return fmt.Errorf("计算新二进制校验和: %w", err)
	}
	if err := writeFilePrivate(p.Sum, []byte(binSum)); err != nil {
		return fmt.Errorf("写入校验和: %w", err)
	}
	if err := os.Rename(binPath, p.New); err != nil {
		_ = os.Remove(p.Sum)
		return fmt.Errorf("暂存新版本: %w", err)
	}

	if err := writeMarker(p.Marker, marker{
		From:     currentVersion,
		To:       strings.TrimPrefix(rel.TagName, "v"),
		StagedAt: time.Now().Unix(),
	}); err != nil {
		cleanStaged(p)
		return fmt.Errorf("写入升级标记: %w", err)
	}

	prog(PhaseStaged, 100, "新版本已就绪，正在重启…")
	return nil
}

// fetchSums 下载并解析 SHA256SUMS，返回 文件名 → 十六进制摘要。
func fetchSums(ctx context.Context, c *http.Client, rel *Release) (map[string]string, error) {
	asset, ok := rel.FindAsset(sumsAsset)
	if !ok {
		return nil, fmt.Errorf("该 Release 没有 %s，无法校验完整性，拒绝升级", sumsAsset)
	}
	body, err := get(ctx, c, asset.URL)
	if err != nil {
		return nil, fmt.Errorf("下载 %s: %w", sumsAsset, err)
	}
	defer body.Close()

	raw, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取 %s: %w", sumsAsset, err)
	}
	out := parseSums(string(raw))
	if len(out) == 0 {
		return nil, fmt.Errorf("%s 内容为空或格式无法识别", sumsAsset)
	}
	return out, nil
}

// parseSums 解析 sha256sum 风格的清单，返回 文件名 → 十六进制摘要。
//
// 第一个字段必须是 64 位十六进制才收录。只按"恰好两个字段"判断是不够的——
// 任意一行两个单词的说明文字都会被当成合法条目，把垃圾值塞进摘要表，
// 真正的资产反而可能匹配到错误的摘要。
func parseSums(raw string) map[string]string {
	out := map[string]string{}
	for line := range strings.Lines(raw) {
		// 格式为 "<sha256>  <filename>"（sha256sum 用双空格；shasum 的二进制
		// 模式会给文件名加 * 前缀）。
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 || !isHexSHA256(fields[0]) {
			continue
		}
		name := strings.TrimPrefix(fields[1], "*")
		if name == "" {
			continue
		}
		out[name] = strings.ToLower(fields[0])
	}
	return out
}

func isHexSHA256(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return true
}

// download 向已独占创建的文件写入限量资产，同时计算 SHA256 并按声明大小汇报进度。
func download(ctx context.Context, c *http.Client, a Asset, f *os.File, budget int64, prog Progress) (string, error) {
	if a.Size > budget {
		return "", fmt.Errorf("发布包声明大小 %s 超过下载上限 %s", humanSize(a.Size), humanSize(budget))
	}
	limit := budget
	if a.Size > 0 && a.Size < limit {
		limit = a.Size
	}
	body, err := get(ctx, c, a.URL)
	if err != nil {
		return "", fmt.Errorf("下载 %s: %w", a.Name, err)
	}
	defer body.Close()

	h := sha256.New()
	pw := &progressWriter{total: a.Size, prog: prog, name: a.Name, last: time.Now()}
	if _, err := io.Copy(io.MultiWriter(f, h, pw), io.LimitReader(body, limit+1)); err != nil {
		return "", fmt.Errorf("下载中断: %w", err)
	}
	if pw.written > limit {
		return "", fmt.Errorf("发布包下载超过上限 %d 字节（已读取 %d 字节）", limit, pw.written)
	}
	if err := f.Sync(); err != nil {
		return "", fmt.Errorf("落盘失败: %w", err)
	}
	if a.Size > 0 && pw.written != a.Size {
		return "", fmt.Errorf("下载不完整：期望 %d 字节，实际 %d 字节", a.Size, pw.written)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// get 发起一个受白名单约束的 GET，返回响应体。
func get(ctx context.Context, c *http.Client, rawURL string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if err := checkURL(req.URL); err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "artex-selfupdate")
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp.Body, nil
}

// extractBinary 从发布包里取出 artex 可执行文件。
//
// 包内结构是 artex-<版本>-<os>-<arch>/artex，但这里按**基名**匹配而不是拼完整
// 路径：版本号在包名里出现过一次，拼错一个字符就整个升级失败，按基名找更耐改。
func extractBinary(zipPath string, f *os.File) error {
	want := "artex"
	if runtime.GOOS == "windows" {
		want = "artex.exe"
	}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return fmt.Errorf("打开发布包: %w", err)
	}
	defer zr.Close()

	for _, entry := range zr.File {
		if entry.FileInfo().IsDir() || !strings.EqualFold(path.Base(entry.Name), want) {
			continue
		}
		rc, err := entry.Open()
		if err != nil {
			return fmt.Errorf("读取 %s: %w", entry.Name, err)
		}
		defer rc.Close()

		n, err := io.Copy(f, io.LimitReader(rc, maxBinarySize+1))
		if err != nil {
			return fmt.Errorf("解压 %s: %w", entry.Name, err)
		}
		if n > maxBinarySize {
			return fmt.Errorf("发布包内的可执行文件超过 %s，拒绝解压", humanSize(maxBinarySize))
		}
		if n == 0 {
			return fmt.Errorf("发布包内的 %s 是空文件", want)
		}
		return f.Sync()
	}
	return fmt.Errorf("发布包里没有找到 %s", want)
}

// checkWritable 提前确认目录可写。没有这一步，非 root 运行、或二进制被放在系统
// 目录时，会在下载完几十 MB 之后才在换装那一刻失败。
func checkWritable(dir string) error {
	probe, err := os.CreateTemp(dir, ".artex-update-probe-*")
	if err != nil {
		return fmt.Errorf("程序目录 %s 不可写，无法自动更新（请检查权限或改用手动升级）: %w", dir, err)
	}
	name := probe.Name()
	_ = probe.Close()
	_ = os.Remove(name)
	return nil
}

// progressWriter 统计已写字节并限频汇报，避免每个 32KiB 分块都推一条 SSE。
type progressWriter struct {
	total   int64
	written int64
	name    string
	prog    Progress
	last    time.Time
}

func (w *progressWriter) Write(b []byte) (int, error) {
	w.written += int64(len(b))
	if time.Since(w.last) < 300*time.Millisecond {
		return len(b), nil
	}
	w.last = time.Now()
	pct := -1
	if w.total > 0 {
		pct = int(w.written * 100 / w.total)
	}
	w.prog(PhaseDownload, pct, fmt.Sprintf("下载中 %s / %s", humanSize(w.written), humanSize(w.total)))
	return len(b), nil
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

func short(sum string) string {
	if len(sum) > 12 {
		return sum[:12] + "…"
	}
	return sum
}
