package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"
)

// 固定文件和真实子进程用于本地前后对比，不依赖模型或外部服务。
func BenchmarkLocalTools(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "fixture.txt")
	if err := os.WriteFile(path, []byte(strings.Repeat("match line\nother line\n", 400000)), 0600); err != nil {
		b.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": path, "limit": 20})
	b.Run("Read20Lines8MiB", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if r, err := runRead(context.Background(), raw, nil); err != nil || r.IsError {
				b.Fatal(err, r.Flatten())
			}
		}
	})
	re := regexp.MustCompile("match")
	b.Run("GrepCount8MiB", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			if r := scanFile(path, dir, re, "count", true, false, 0, 0); r == nil || r.count != 400000 {
				b.Fatal(r)
			}
		}
	})
	b.Run("BashOutput8MiB", func(b *testing.B) {
		if runtime.GOOS == "windows" {
			b.Skip("POSIX cat fixture")
		}
		b.ReportAllocs()
		for b.Loop() {
			if r, err := runBashSync(context.Background(), nil, "cat "+path, time.Minute); err != nil || r.IsError {
				b.Fatal(err, r.Flatten())
			}
		}
	})
}
