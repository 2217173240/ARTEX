package selfupdate

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
)

func buildNativeHelper(t *testing.T, dir, version string) string {
	t.Helper()
	name := "artex"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(dir, name)
	cmd := exec.Command("go", "build", "-ldflags=-X main.version="+version, "-o", path, "./testdata/updater-helper")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build native helper: %v\n%s", err, out)
	}
	return path
}

func runNativeHelper(t *testing.T, bin string, code int, args ...string) []byte {
	t.Helper()
	cmd := exec.Command(bin, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	actual := 0
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			actual = exit.ExitCode()
		} else {
			t.Fatalf("run helper %v: %v\n%s", args, err, stderr.String())
		}
	}
	if actual != code {
		t.Fatalf("helper %v exit=%d want=%d\n%s\n%s", args, actual, code, out, stderr.String())
	}
	return out
}

func nativePaths(t *testing.T, bin string) Paths {
	t.Helper()
	var p Paths
	if err := json.Unmarshal(runNativeHelper(t, bin, 0, "paths"), &p); err != nil {
		t.Fatal(err)
	}
	return p
}

func nativeState(t *testing.T, bin, command, version string, pending bool) {
	t.Helper()
	var got struct {
		Version string
		State   State
	}
	if err := json.Unmarshal(runNativeHelper(t, bin, 0, command), &got); err != nil {
		t.Fatal(err)
	}
	if got.Version != version || got.State.Pending != pending {
		t.Fatalf("helper state=%+v want version=%s pending=%v", got, version, pending)
	}
}

func zipNativeHelper(t *testing.T, bin string) []byte {
	t.Helper()
	data, err := os.ReadFile(bin)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	zw := zip.NewWriter(&out)
	w, err := zw.Create("portable/" + filepath.Base(bin))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return out.Bytes()
}

func TestNativePortableLifecycle(t *testing.T) {
	// 每轮都通过独立原生进程执行公共入口，目录仅包含本测试的 helper 与发布件。
	installDir := t.TempDir()
	current := buildNativeHelper(t, installDir, "1.0.0")
	newBinary := buildNativeHelper(t, t.TempDir(), "2.0.0")
	p := nativePaths(t, current)
	if runtime.GOOS == "windows" && (!strings.HasSuffix(p.New, ".new.exe") || !strings.HasSuffix(p.Old, ".old.exe")) {
		t.Fatalf("Windows paths lost executable suffix: %+v", p)
	}
	payload := zipNativeHelper(t, newBinary)
	name := AssetName("v2.0.0", runtime.GOOS, runtime.GOARCH)
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	var requests atomic.Int32
	s := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		switch r.URL.Path {
		case "/SHA256SUMS":
			fmt.Fprintf(w, "%s  %s\n", sum, name)
		case "/" + name:
			_, _ = w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
	defer s.Close()
	rel := Release{TagName: "v2.0.0", Assets: []Asset{
		{Name: name, URL: "https://github.com/" + name, Size: int64(len(payload))},
		{Name: sumsAsset, URL: "https://github.com/SHA256SUMS"},
	}}
	relPath := filepath.Join(t.TempDir(), "release.json")
	relJSON, err := json.Marshal(rel)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relPath, relJSON, 0o600); err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(t.TempDir(), "local-origin.pem")
	if err := os.WriteFile(certPath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: s.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	stageArgs := []string{"stage", relPath, certPath, s.Listener.Addr().String()}
	for _, blocked := range []string{p.Sum, p.Marker} {
		if err := os.Mkdir(blocked, 0o700); err != nil {
			t.Fatal(err)
		}
		runNativeHelper(t, current, 1, stageArgs...)
		for _, path := range []string{p.New, p.Sum} {
			if path == blocked {
				continue
			}
			if _, err := os.Lstat(path); !os.IsNotExist(err) {
				t.Fatalf("failed metadata publication retained %s: %v", path, err)
			}
		}
		if err := os.Remove(blocked); err != nil {
			t.Fatal(err)
		}
		nativeState(t, current, "bootstrap", "1.0.0", false)
	}
	legacyLinks := map[string]string{}
	for _, path := range []string{p.New + ".zip.part", p.New + ".part"} {
		target := path + ".canary"
		if err := os.WriteFile(target, []byte("fixed-path-canary"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			if runtime.GOOS == "windows" {
				t.Logf("symlink privilege unavailable; native lifecycle continues: %v", err)
				continue
			}
			t.Fatal(err)
		}
		legacyLinks[path] = target
	}
	for _, path := range []string{p.Sum, p.Marker} {
		if err := os.WriteFile(path, []byte("stale metadata"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runNativeHelper(t, current, ExitRestart, stageArgs...)
	if err := verifyStaged(p); err != nil {
		t.Fatal(err)
	}
	pending := map[string]string{}
	for _, path := range []string{p.New, p.Sum, p.Marker} {
		pending[path] = readAll(t, path)
	}
	beforeRequests := requests.Load()
	runNativeHelper(t, current, 1, stageArgs...)
	if requests.Load() != beforeRequests {
		t.Fatal("repeat Stage made a network request while update was pending")
	}
	for path, want := range pending {
		if readAll(t, path) != want {
			t.Fatalf("repeat Stage changed valid pending update %s", path)
		}
	}
	if info, err := os.Lstat(p.Sum); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("invalid staged checksum: %v %v", info, err)
	}
	runNativeHelper(t, current, ExitRestart, "bootstrap")
	if _, err := os.Stat(p.New); !os.IsNotExist(err) {
		t.Fatal("Bootstrap retained staged executable")
	}
	nativeState(t, current, "settle", "2.0.0", true)
	if _, ok := readMarker(p.Marker); ok {
		t.Fatal("Settle retained marker")
	}
	nativeState(t, current, "bootstrap", "2.0.0", false)
	runNativeHelper(t, current, ExitRestart, "rollback")
	nativeState(t, current, "bootstrap", "1.0.0", false)
	backupSum, err := fileSHA256(p.Old)
	if err != nil {
		t.Fatal(err)
	}
	wantNewSum, err := fileSHA256(newBinary)
	if err != nil || backupSum != wantNewSum {
		t.Fatalf("manual rollback did not retain new backup: %s %s %v", backupSum, wantNewSum, err)
	}
	// 第二轮换装替换已有备份，并通过实际启动次数触发自动回滚。
	metadataTargets := map[string]string{}
	for _, path := range []string{p.Sum, p.Marker} {
		target := path + ".canary"
		if err := os.WriteFile(target, []byte("metadata-canary"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, path); err != nil {
			if runtime.GOOS == "windows" {
				t.Logf("symlink privilege unavailable; native lifecycle continues: %v", err)
				continue
			}
			t.Fatal(err)
		}
		metadataTargets[path] = target
	}
	runNativeHelper(t, current, ExitRestart, stageArgs...)
	for path, target := range metadataTargets {
		if readAll(t, target) != "metadata-canary" {
			t.Fatalf("Stage followed metadata symlink %s", path)
		}
		if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("Stage did not replace metadata symlink %s: %v %v", path, info, err)
		}
	}
	runNativeHelper(t, current, ExitRestart, "bootstrap")
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		nativeState(t, current, "bootstrap", "2.0.0", true)
		m, ok := readMarker(p.Marker)
		if !ok || m.Attempts != attempt {
			t.Fatalf("attempt=%d marker=%+v readable=%v", attempt, m, ok)
		}
	}
	runNativeHelper(t, current, ExitRestart, "bootstrap")
	nativeState(t, current, "bootstrap", "1.0.0", false)
	failedSum, err := fileSHA256(p.Current + ".failed")
	if err != nil || failedSum != wantNewSum {
		t.Fatalf("failed binary missing or changed: %s %s %v", failedSum, wantNewSum, err)
	}
	if _, ok := readMarker(p.Marker); ok {
		t.Fatal("automatic rollback retained marker")
	}
	for _, pattern := range []string{".artex-update-*"} {
		matches, err := filepath.Glob(filepath.Join(p.Dir, pattern))
		if err != nil || len(matches) != 0 {
			t.Fatalf("temporary files remain: %v %v", matches, err)
		}
	}
	for path, target := range legacyLinks {
		if readAll(t, target) != "fixed-path-canary" {
			t.Fatalf("Stage followed legacy intermediate symlink %s", path)
		}
		if info, err := os.Lstat(path); err != nil || info.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("Stage cleaned unowned intermediate %s: %v %v", path, info, err)
		}
	}
}

func TestSmokeEnvironmentNative(t *testing.T) {
	dir := t.TempDir()
	bin := buildNativeHelper(t, dir, "1.0.0")
	for _, key := range []string{"ARTEX_HOME", "ARTEX_CONFIG", "ARTEX_TEST_SECRET", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "GODEBUG"} {
		t.Setenv(key, "selfupdate-harmless-canary")
	}
	t.Setenv(smokeEnv, "parent-marker")
	t.Setenv("HOME", filepath.Join(dir, "home"))
	before := os.Environ()
	if err := smokeTest(bin); err != nil {
		t.Fatal(err)
	}
	var env []string
	if err := json.Unmarshal([]byte(readAll(t, filepath.Join(dir, "smoke-environment.json"))), &env); err != nil {
		t.Fatal(err)
	}
	projection := exec.Command(bin, "-h")
	projection.Env = smokeEnvironment(before, runtime.GOOS)
	want := map[string]string{}
	for _, entry := range projection.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		want[key] = value
	}
	allowed := map[string]bool{
		"PATH": true, "HOME": true, "TMPDIR": true, "TEMP": true,
		"TMP": true, "SystemRoot": true, "USERPROFILE": true, smokeEnv: true,
	}
	if runtime.GOOS == "windows" {
		delete(allowed, "SystemRoot")
		allowed["SYSTEMROOT"] = true
	}
	actual := map[string]string{}
	for _, entry := range env {
		key, value, _ := strings.Cut(entry, "=")
		if runtime.GOOS == "windows" {
			key = strings.ToUpper(key)
		}
		if !allowed[key] {
			t.Fatalf("child environment contains unexpected key %q", key)
		}
		actual[key] = value
	}
	for key, value := range want {
		if got, exists := actual[key]; !exists || got != value {
			t.Fatalf("child environment changed allowed key %s", key)
		}
	}
	if strings.Join(os.Environ(), "\n") != strings.Join(before, "\n") {
		t.Fatal("smoke test changed parent environment")
	}
	if err := os.WriteFile(filepath.Join(dir, "smoke-error"), []byte("fail help"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := smokeTest(bin); err == nil || !strings.Contains(err.Error(), "native help failure") {
		t.Fatalf("native help failure not observable: %v", err)
	}
}
