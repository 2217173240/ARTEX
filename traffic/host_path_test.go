package traffic

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHostTreePathDirectoryComponent(t *testing.T) {
	root := t.TempDir()
	tr := &Traffic{dir: root}
	for _, tc := range []struct{ host, want string }{
		{".", ""},
		{"..", ""},
		{"/", "root"},
		{`\`, "root"},
		{"../outside", ".._outside"},
		{`..\outside`, ".._outside"},
		{"example.com", "example.com"},
		{"root", "root"},
		{"example.com:443", "example.com_443"},
		{"127.0.0.1:8080", "127.0.0.1_8080"},
		{"[2001:db8::1]:443", "[2001_db8__1]_443"},
	} {
		t.Run(tc.host, func(t *testing.T) {
			path, ok := tr.hostTreePath(tc.host)
			if tc.want == "" {
				if ok || path != "" {
					t.Fatalf("host %q produced legacy tree %q", tc.host, path)
				}
				return
			}
			if !ok {
				t.Fatalf("valid host %q has no legacy tree", tc.host)
			}
			component := filepath.Base(path)
			if component != tc.want {
				t.Fatalf("host component(%q) = %q; want %q", tc.host, component, tc.want)
			}
			rel, err := filepath.Rel(root, filepath.Join(root, component))
			if err != nil || rel == "." || rel == ".." || strings.ContainsAny(rel, `/\`) || filepath.IsAbs(rel) {
				t.Fatalf("host %q does not resolve to one child component: %q (%v)", tc.host, rel, err)
			}
		})
	}
}

func TestSpecialHostTreeStagingStaysWithinTrafficRoot(t *testing.T) {
	for _, host := range []string{".", "..", "../outside", `..\outside`, "[2001:db8::1]:443"} {
		t.Run(host, func(t *testing.T) {
			parent := t.TempDir()
			root := filepath.Join(parent, "traffic")
			if err := os.Mkdir(root, 0o700); err != nil {
				t.Fatal(err)
			}
			outside := filepath.Join(parent, "outside")
			if err := os.WriteFile(outside, []byte("untouched"), 0o600); err != nil {
				t.Fatal(err)
			}
			tr := &Traffic{dir: root}
			// Both host-directory builders use this path helper. Missing host
			// directories must not cause the root or its parent to be staged.
			source, ok := tr.hostTreePath(host)
			var sources []string
			if ok {
				sources = append(sources, source)
			}
			stageDir, moves, err := tr.stageTrees(sources)
			if err != nil || stageDir != "" || len(moves) != 0 {
				t.Fatalf("missing host tree staging = (%q, %v, %v)", stageDir, moves, err)
			}
			if !ok {
				got, err := os.ReadFile(outside)
				if err != nil || string(got) != "untouched" {
					t.Fatalf("outside file changed: %q, %v", got, err)
				}
				return
			}
			if err := os.Mkdir(source, 0o700); err != nil {
				t.Fatal(err)
			}
			body := filepath.Join(source, "request.http")
			if err := os.WriteFile(body, []byte("capture"), 0o600); err != nil {
				t.Fatal(err)
			}
			stageDir, moves, err = tr.stageTrees([]string{source})
			if err != nil || len(moves) != 1 {
				t.Fatalf("existing host tree staging = (%q, %v, %v)", stageDir, moves, err)
			}
			if err := restoreTrees(stageDir, moves); err != nil {
				t.Fatal(err)
			}
			for path, want := range map[string]string{outside: "untouched", body: "capture"} {
				got, err := os.ReadFile(path)
				if err != nil || string(got) != want {
					t.Fatalf("read %s = %q, %v; want %q", path, got, err, want)
				}
			}
		})
	}
}

func TestDotHostDeletionPreservesRootHost(t *testing.T) {
	tr, dir := openTraffic(t)
	rootTree := filepath.Join(dir, "root")
	if err := os.Mkdir(rootTree, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(rootTree, "request.http")
	if err := os.WriteFile(marker, []byte("root capture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"root", ".", ".."} {
		if _, err := tr.DB().Exec(`INSERT INTO exchanges(id,ts,host,method,url_template,url,status,content_type,req_len,resp_len,path) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, host, 1, host, "GET", "/", "http://"+host+"/", 200, "text/plain", 0, 0, ""); err != nil {
			t.Fatal(err)
		}
	}
	// The substring-delete builder must also exclude dot components.
	paths, err := tr.hostTrees(`host IN (?,?)`, ".", "..")
	if err != nil || len(paths) != 0 {
		t.Fatalf("dot host trees = %v, %v", paths, err)
	}
	stage, err := tr.StageDeleteHostsExact([]string{".", ".."})
	if err != nil {
		t.Fatal(err)
	}
	if stage.Deleted() != 2 {
		t.Fatalf("deleted = %d; want 2", stage.Deleted())
	}
	if err := stage.Commit(); err != nil {
		t.Fatal(err)
	}
	tr.reaping.Wait()
	var remaining string
	if err := tr.DB().QueryRow(`SELECT host FROM exchanges`).Scan(&remaining); err != nil || remaining != "root" {
		t.Fatalf("remaining host = %q, %v; want root", remaining, err)
	}
	if got, err := os.ReadFile(marker); err != nil || string(got) != "root capture" {
		t.Fatalf("root host tree changed: %q, %v", got, err)
	}
}
