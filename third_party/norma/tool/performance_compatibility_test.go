package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestCaptureStreamCompatibility(t *testing.T) {
	for _, limit := range []int{1, 2, 7, 30000} {
		for _, data := range []string{"", "a", "\n", strings.Repeat("中文\n", 10000)} {
			for _, chunk := range []int{1, 37, 8192, 100000} {
				tc := &ToolContext{MaxOutputChars: limit}
				w := newCaptureWriter(tc)
				for at := 0; at < len(data); at += chunk {
					_, _ = w.Write([]byte(data[at:min(at+chunk, len(data))]))
				}
				if got, want := w.finish(), Capture(tc, data); got != want {
					t.Fatalf("limit=%d chunk=%d changed output", limit, chunk)
				}
			}
		}
	}
}

func TestCaptureStreamRealCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX cat, printf and exec fixture")
	}
	dir := t.TempDir()
	data := strings.Repeat("stdout line\n", 100000)
	path := filepath.Join(dir, "input")
	if err := os.WriteFile(path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	for _, broken := range []bool{false, true} {
		output := filepath.Join(dir, fmt.Sprint("output-", broken))
		if broken {
			if err := os.WriteFile(output, []byte("occupied"), 0600); err != nil {
				t.Fatal(err)
			}
		}
		tc := &ToolContext{WorkingDir: dir, OutputDir: output, MaxOutputChars: 100}
		r, err := runBashSync(t.Context(), tc, "cat input; printf 'stderr line\\n' >&2; exit 7", time.Second*5)
		if err != nil || !r.IsError || !strings.Contains(r.Flatten(), "[exit code 7]") {
			t.Fatal(err, r.Flatten())
		}
		if broken {
			if !strings.Contains(r.Flatten(), "characters truncated") {
				t.Fatal(r.Flatten())
			}
			continue
		}
		entries, err := os.ReadDir(output)
		if err != nil || len(entries) != 1 {
			t.Fatal(err, entries)
		}
		got, err := os.ReadFile(filepath.Join(output, entries[0].Name()))
		if err != nil || string(got) != data+"stderr line\n" {
			t.Fatal("artifact changed", err)
		}
		if !strings.Contains(r.Flatten(), "full 1200012 bytes / 100002 lines") {
			t.Fatal(r.Flatten())
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	r, err := runBashSync(ctx, nil, "exec sleep 30", time.Minute)
	if err != nil || !r.IsError {
		t.Fatal("cancelled command succeeded", err, r.Flatten())
	}
	r, err = runBashSync(t.Context(), nil, "exec sleep 30", 10*time.Millisecond)
	if err != nil || !r.IsError || !strings.Contains(r.Flatten(), "timed out") {
		t.Fatal("missing timeout", err, r.Flatten())
	}
}

func TestReadStreamingCompatibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data")
	for _, data := range []string{"", "\n", "a", "a\n", "a\n\nb", "a\r\nb", strings.Repeat("x", 2000) + "\n", strings.Repeat("x", 2001), strings.Repeat("x", 100000) + "\nafter\n", "binary\x00data", strings.Repeat("row\n", 20000)} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		for _, offset := range []int{0, 1, 2, 5, 21000} {
			for _, limit := range []int{0, 1, 2, 20} {
				raw, _ := json.Marshal(map[string]any{"file_path": path, "offset": offset, "limit": limit})
				r, err := runRead(t.Context(), raw, nil)
				if err != nil {
					t.Fatal(err)
				}
				var want string
				if isBinary([]byte(data)) {
					want = fmt.Sprintf("[binary file %s not shown — %d bytes]", path, len(data))
				} else {
					lines := strings.Split(data, "\n")
					start := min(max(offset-1, 0), len(lines))
					n := limit
					if n <= 0 {
						n = 2000
					}
					var out strings.Builder
					for i := start; i < min(start+n, len(lines)); i++ {
						line := lines[i]
						if len(line) > maxLineLen {
							line = line[:maxLineLen] + "… [line truncated]"
						}
						fmt.Fprintf(&out, "%6d\t%s\n", i+1, line)
					}
					want = out.String()
					if want == "" {
						if data == "" {
							want = "(file is empty)"
						} else {
							want = "(no lines in the requested range)"
						}
					} else {
						want = readCapture(nil, want, start)
					}
				}
				if r.Flatten() != want {
					t.Fatalf("Read changed bytes=%d offset=%d limit=%d\ngot=%q\nwant=%q", len(data), offset, limit, r.Flatten(), want)
				}
			}
		}
	}
	if err := os.WriteFile(path, []byte(strings.Repeat("x", 2001)+"\nnext\n"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(map[string]any{"file_path": path})
	tc := &ToolContext{MaxOutputChars: 1, OutputDir: filepath.Join(dir, "spill")}
	r, err := runRead(t.Context(), raw, tc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(r.Flatten(), "offset=2") || CaptureToolOutput("Read", tc, r.Flatten()) != r.Flatten() {
		t.Fatal("lost forward cursor", r.Flatten())
	}
	if _, err := os.Stat(tc.OutputDir); !os.IsNotExist(err) {
		t.Fatal("Read spilled a copy", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := runRead(ctx, raw, nil); !errors.Is(err, context.Canceled) {
		t.Fatal("Read ignored cancellation", err)
	}
}

// 原始 v0.4.3 搜索规则作为固定文件兼容参照。
func referenceSearch(path, base string, re *regexp.Regexp, mode string, numbers, multi bool, before, after int) *grepHit {
	data, err := os.ReadFile(path)
	if err != nil || isBinary(data) {
		return nil
	}
	rel, _ := filepath.Rel(base, path)
	lines := strings.Split(string(data), "\n")
	if len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	var matches []int
	if multi {
		starts := make([]int, len(lines))
		off := 0
		for i, line := range lines {
			starts[i] = off
			off += len(line) + 1
		}
		hit := make([]bool, len(lines))
		for _, loc := range re.FindAllIndex(data, -1) {
			for i, line := range lines {
				if loc[0] <= starts[i]+len(line) && loc[1] > starts[i] {
					hit[i] = true
				}
			}
		}
		for i, h := range hit {
			if h {
				matches = append(matches, i)
			}
		}
	} else {
		for i, line := range lines {
			if re.MatchString(line) {
				matches = append(matches, i)
			}
		}
	}
	if len(matches) == 0 {
		return nil
	}
	out := &grepHit{path: rel, count: len(matches)}
	if mode != "content" {
		return out
	}
	matched := map[int]bool{}
	seen := map[int]bool{}
	var order []int
	for _, m := range matches {
		matched[m] = true
		for j := max(0, m-before); j <= min(len(lines)-1, m+after); j++ {
			if !seen[j] {
				seen[j] = true
				order = append(order, j)
			}
		}
	}
	sort.Ints(order)
	prev := -2
	for _, j := range order {
		if (before > 0 || after > 0) && prev >= 0 && j != prev+1 {
			out.lines = append(out.lines, "--")
		}
		sep := "-"
		if matched[j] {
			sep = ":"
		}
		if numbers {
			out.lines = append(out.lines, fmt.Sprintf("%s%s%d%s%s", rel, sep, j+1, sep, lines[j]))
		} else {
			out.lines = append(out.lines, rel+sep+lines[j])
		}
		prev = j
	}
	return out
}

func TestSearchStreamingCompatibility(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fixture")
	for _, data := range []string{"", "\n", "x", "x\n", "x\ny\n", "a\n\nx\nxyz\n中文\n", strings.Repeat("long", 20000) + "x\ny\n", "x\x00binary"} {
		if err := os.WriteFile(path, []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		for _, pattern := range []string{"x", "a.*x", "(?s)x.*?y", "^", "$", "", "\\n", "x\\ny", "中文", "\\b"} {
			for _, multi := range []bool{false, true} {
				for _, mode := range []string{"files_with_matches", "count", "content"} {
					for _, window := range [][2]int{{0, 0}, {1, 2}, {-1, 2}, {2, -1}, {-1, -1}, {20, 20}} {
						for _, numbers := range []bool{false, true} {
							re := regexp.MustCompile(pattern)
							got := scanFile(path, dir, re, mode, numbers, multi, window[0], window[1])
							want := referenceSearch(path, dir, re, mode, numbers, multi, window[0], window[1])
							if !reflect.DeepEqual(got, want) {
								t.Fatalf("search changed pattern=%q multi=%v mode=%s window=%v data length=%d\ngot=%+v\nwant=%+v", pattern, multi, mode, window, len(data), got, want)
							}
						}
					}
				}
			}
		}
	}
}

func TestLinePagerCompatibility(t *testing.T) {
	for _, data := range []string{"", "\n", "\n\n", "one", "one\n", "one\n\n", "\none\n\ntwo\n\n", strings.Repeat("x", 80000) + "\ny\nz\n", strings.Repeat("row\n", 1000)} {
		for _, limit := range []int{-1, 0, 1, 3, 250} {
			for _, offset := range []int{-2, 0, 1, 2, 10001} {
				for _, chunk := range []int{1, 7, 4096, 90000} {
					p := &linePager{limit: limit, offset: max(0, offset)}
					for at := 0; at < len(data); at += chunk {
						_, _ = p.Write([]byte(data[at:min(at+chunk, len(data))]))
					}
					got, more := p.finish()
					var lines []string
					if text := strings.TrimRight(data, "\n"); text != "" {
						lines = strings.Split(text, "\n")
					}
					want, wmore := applyHeadOffset(lines, limit, offset)
					if strings.Join(got, "\n") != strings.Join(want, "\n") || len(got) != len(want) || more != wmore {
						t.Fatalf("pager changed limit=%d offset=%d chunk=%d", limit, offset, chunk)
					}
				}
			}
		}
	}
	p := &linePager{limit: 1, offset: 1}
	chunk := []byte(strings.Repeat("x", 4096))
	for range 1000 {
		_, _ = p.Write(chunk)
	}
	if p.pending.Len() != 0 {
		t.Fatal("retained skipped line")
	}
}

func TestRealRipgrepPaginationAndCancellation(t *testing.T) {
	rg, err := exec.LookPath("rg")
	if err != nil {
		t.Skip("rg is unavailable")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "data.txt"), []byte(strings.Repeat("match\nother\n", 10000)), 0600); err != nil {
		t.Fatal(err)
	}
	limit := 2
	r := ripgrepReq{pattern: "match", root: dir, mode: "content", showLines: true, headLimit: &limit, offset: 1}
	got, ok := runWithRipgrep(t.Context(), rg, r, nil)
	if !ok || !strings.Contains(strings.ReplaceAll(got.Flatten(), "\\", "/"), "./data.txt:3:match\n./data.txt:5:match\n") || !strings.Contains(got.Flatten(), "9997 more result(s)") {
		t.Fatal(ok, got.Flatten())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, ok := runWithRipgrep(ctx, rg, r, nil); ok {
		t.Fatal("cancelled rg succeeded")
	}
}
