package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Autumn-27/artex/config"
	"github.com/Autumn-27/artex/distribution"
	"github.com/jackc/pgx/v5"
)

type launcherState struct {
	Address string `json:"address"`
	Token   string `json:"token"`
}
type launcherStatus struct {
	URL        string `json:"url"`
	Phase      string `json:"phase"`
	Message    string `json:"message"`
	Log        string `json:"log"`
	ControlURL string `json:"control_url"`
}

func launcherHome() (string, error) {
	if s := os.Getenv("ARTEX_HOME"); s != "" {
		return filepath.Abs(s)
	}
	return distribution.UserHome()
}
func launcherToken() string {
	b := make([]byte, 32)
	if _, e := rand.Read(b); e != nil {
		panic(e)
	}
	return hex.EncodeToString(b)
}
func launcherRequest(s launcherState, method, path string) (*http.Response, error) {
	u := "http://" + s.Address + path
	host, _, e := net.SplitHostPort(s.Address)
	if e != nil || host != "127.0.0.1" {
		return nil, errors.New("invalid launcher address")
	}
	r, e := http.NewRequest(method, u, nil)
	if e != nil {
		return nil, e
	}
	r.Header.Set("Authorization", "Bearer "+s.Token)
	return (&http.Client{Timeout: 2 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(r)
}
func runLauncher(args []string, stdout, stderr io.Writer) int {
	noBrowser := false
	filtered := args[:0]
	for _, arg := range args {
		if arg == "--no-browser" {
			noBrowser = true
		} else {
			filtered = append(filtered, arg)
		}
	}
	args = filtered
	if noBrowser {
		os.Setenv("ARTEX_LAUNCH_NO_BROWSER", "1")
	}
	home, e := launcherHome()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if e = os.MkdirAll(home, 0700); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	if len(args) > 0 && args[0] == "serve" {
		return launcherServe(home, stdout, stderr)
	}
	if len(args) > 1 || len(args) == 1 && args[0] != "stop" && args[0] != "status" && args[0] != "control" {
		fmt.Fprintln(stderr, "usage: artex launch [stop|status|control] [--no-browser]")
		return 2
	}
	statePath := filepath.Join(home, "launcher.json")
	var s launcherState
	b, _ := os.ReadFile(statePath)
	_ = json.Unmarshal(b, &s)
	if len(args) > 0 && args[0] == "control" {
		resp, e := launcherRequest(s, "GET", "/status")
		if e != nil {
			fmt.Fprintln(stderr, "No running ARTEX launcher found.")
			return 1
		}
		defer resp.Body.Close()
		var st launcherStatus
		if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&st) != nil {
			return 1
		}
		launcherOpen(st.ControlURL)
		fmt.Fprintln(stdout, st.ControlURL)
		return 0
	}
	if len(args) > 0 && args[0] == "status" {
		resp, e := launcherRequest(s, "GET", "/status")
		if e != nil {
			json.NewEncoder(stdout).Encode(launcherStatus{Phase: "stopped", Log: filepath.Join(home, "backend.log")})
			return 1
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return 1
		}
		var st launcherStatus
		if json.NewDecoder(resp.Body).Decode(&st) != nil {
			return 1
		}
		json.NewEncoder(stdout).Encode(st)
		if st.Phase == "failed" {
			return 1
		}
		return 0
	}
	if len(args) > 0 {
		resp, e := launcherRequest(s, "POST", "/stop")
		if e != nil {
			fmt.Fprintln(stderr, "No running ARTEX launcher was found.")
			return 1
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return 1
		}
		for deadline := time.Now().Add(8 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
			if _, err := os.Stat(statePath); os.IsNotExist(err) {
				fmt.Fprintln(stdout, "ARTEX stopped.")
				return 0
			}
		}
		fmt.Fprintln(stderr, "ARTEX stop was requested, but shutdown has not completed. See", filepath.Join(home, "launcher.log"))
		return 1
	}
	if resp, e := launcherRequest(s, "GET", "/status"); e == nil {
		var st launcherStatus
		_ = json.NewDecoder(resp.Body).Decode(&st)
		resp.Body.Close()
		if resp.StatusCode == 200 && st.URL != "" {
			if st.Phase == "failed" {
				st.URL = st.ControlURL
			}
			launcherOpen(st.URL)
			fmt.Fprintln(stdout, st.URL)
			if st.Phase == "failed" {
				fmt.Fprintln(stderr, st.Message, "See", st.Log)
				return 1
			}
			return 0
		}
	}
	exe, e := os.Executable()
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	logPath := filepath.Join(home, "launcher.log")
	log, e := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	defer log.Close()
	cmd := exec.Command(exe, "launch", "serve")
	cmd.Stdout = log
	cmd.Stderr = log
	cmd.Env = launcherEnv(home, os.Environ())
	if e = cmd.Start(); e != nil {
		fmt.Fprintln(stderr, e)
		return 1
	}
	_ = cmd.Process.Release()
	for deadline := time.Now().Add(35 * time.Second); time.Now().Before(deadline); time.Sleep(100 * time.Millisecond) {
		b, _ = os.ReadFile(statePath)
		_ = json.Unmarshal(b, &s)
		if resp, e := launcherRequest(s, "GET", "/status"); e == nil {
			var st launcherStatus
			_ = json.NewDecoder(resp.Body).Decode(&st)
			resp.Body.Close()
			if resp.StatusCode == 200 && st.Phase != "starting" {
				if st.Phase == "failed" {
					st.URL = st.ControlURL
				}
				launcherOpen(st.URL)
				fmt.Fprintln(stdout, st.URL)
				if st.Phase == "failed" {
					fmt.Fprintln(stderr, st.Message, "See", st.Log)
					return 1
				}
				return 0
			}
		}
	}
	fmt.Fprintf(stderr, "ARTEX launcher did not start. See %s\n", logPath)
	return 1
}
func launcherEnv(home string, env []string) []string {
	out := make([]string, 0, len(env)+3)
	for _, v := range env {
		if !strings.HasPrefix(v, "ARTEX_HOME=") && !strings.HasPrefix(v, "ARTEX_CONFIG=") && !strings.HasPrefix(v, "ARTEX_SKILL_DIR=") && !strings.HasPrefix(v, "ARTEX_LAUNCH_PARENT=") && !strings.HasPrefix(v, "ARTEX_LAUNCH_TOKEN=") {
			out = append(out, v)
		}
	}
	cfg := os.Getenv("ARTEX_CONFIG")
	if cfg == "" {
		cfg = filepath.Join(home, "config.json")
	}
	return append(out, "ARTEX_HOME="+home, "ARTEX_CONFIG="+cfg, "ARTEX_SKILL_DIR="+filepath.Join(home, "skills"))
}
func launcherOpen(u string) {
	if os.Getenv("ARTEX_LAUNCH_NO_BROWSER") == "1" {
		return
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		cmd = exec.Command("xdg-open", u)
	}
	if cmd.Start() == nil {
		_ = cmd.Process.Release()
	}
}
func launcherFreeAddress() (string, error) {
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return "", e
	}
	a := l.Addr().String()
	l.Close()
	return a, nil
}

func launcherServe(home string, stdout, stderr io.Writer) int {
	lock, e := launcherLock(filepath.Join(home, "launcher.lock"))
	if e != nil {
		fmt.Fprintln(stderr, "Another ARTEX launcher is active, or the launcher lock could not be opened:", e)
		return 1
	}
	defer lock.Close()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		return 1
	}
	s := launcherState{l.Addr().String(), launcherToken()}
	statePath := filepath.Join(home, "launcher.json")
	b, _ := json.Marshal(s)
	if e = os.WriteFile(statePath, b, 0600); e != nil {
		l.Close()
		return 1
	}
	defer os.Remove(statePath)
	cfg := os.Getenv("ARTEX_CONFIG")
	if cfg == "" {
		cfg = filepath.Join(home, "config.json")
	}
	os.Setenv("ARTEX_CONFIG", cfg)
	exe, _ := os.Executable()
	if e = launcherSeedSkills(launcherSourceSkills(exe), filepath.Join(home, "skills")); e != nil {
		fmt.Fprintln(stderr, "Skill setup:", e)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	var mu sync.Mutex
	ui := "http://" + s.Address + "/"
	message := "连接你已有的 PostgreSQL 数据库，即可启动 ARTEX。"
	form := launcherForm{Host: "localhost", Port: "5432", TLS: "auto"}
	pageError := false
	phase := "setup"
	nonce := launcherToken()
	start := make(chan struct{}, 1)
	if _, _, e := config.PostgresDSN(); e == nil {
		phase = "starting"
		message = "正在启动 ARTEX，请稍候…"
		start <- struct{}{}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Address || r.Header.Get("Authorization") != "Bearer "+s.Token {
			http.Error(w, "forbidden", 403)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		json.NewEncoder(w).Encode(launcherStatus{URL: ui, Phase: phase, Message: message, Log: filepath.Join(home, "backend.log"), ControlURL: "http://" + s.Address + "/"})
	})
	mux.HandleFunc("/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Address || r.Method != "POST" {
			http.Error(w, "forbidden", 403)
			return
		}
		authorized := r.Header.Get("Authorization") == "Bearer "+s.Token
		if !authorized && r.Header.Get("Origin") == "http://"+s.Address {
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.ParseForm() == nil {
				mu.Lock()
				authorized = r.Form.Get("nonce") == nonce
				mu.Unlock()
			}
		}
		if !authorized {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, "ARTEX 已停止，可以关闭此页面。")
		cancel()
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Host != s.Address {
			http.Error(w, "forbidden", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; form-action 'self'; style-src 'unsafe-inline'; frame-ancestors 'none'")
		mu.Lock()
		defer mu.Unlock()
		if r.Method == "POST" {
			if r.Header.Get("Origin") != "http://"+s.Address {
				http.Error(w, "forbidden", 403)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16384)
			if r.ParseForm() != nil || r.Form.Get("nonce") != nonce {
				http.Error(w, "forbidden", 403)
				return
			}
			if phase != "setup" {
				http.Error(w, "数据库已配置，请使用启动器控制页面。", 409)
				return
			}
			form = launcherForm{Host: strings.TrimSpace(r.Form.Get("host")), Port: strings.TrimSpace(r.Form.Get("port")), User: strings.TrimSpace(r.Form.Get("user")), Database: strings.TrimSpace(r.Form.Get("database")), TLS: r.Form.Get("tls")}
			dsn, buildErr := launcherDSN(form, r.Form.Get("password"))
			pageError = true
			if buildErr != nil {
				message = buildErr.Error()
			} else if e := launcherPing(ctx, dsn); e != nil {
				message = "连接失败。请检查地址、端口、账号密码，以及数据库是否允许连接。"
			} else if e = launcherWriteConfig(cfg, dsn); e != nil {
				message = "配置保存失败。已有配置保持不变；请检查文件权限或修改现有 config.json 后重新启动。"
			} else {
				message = "数据库连接成功，正在启动 ARTEX…"
				pageError = false
				phase = "starting"
				nonce = launcherToken()
				select {
				case start <- struct{}{}:
				default:
				}
			}
		} else if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		launcherPage.Execute(w, launcherPageData{Message: message, Nonce: nonce, Log: filepath.Join(home, "backend.log"), Phase: phase, URL: ui, Form: form, Error: pageError || phase == "failed"})
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 15 * time.Second}
	go srv.Serve(l)
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case <-start:
				addr, e := launcherFreeAddress()
				if e != nil {
					mu.Lock()
					phase = "failed"
					message = "无法分配本地端口或打开日志，请检查目录权限。"
					mu.Unlock()
					continue
				}
				proxy, e := launcherFreeAddress()
				if e != nil {
					mu.Lock()
					phase = "failed"
					message = "无法分配本地端口或打开日志，请检查目录权限。"
					mu.Unlock()
					continue
				}
				log, e := os.OpenFile(filepath.Join(home, "backend.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
				if e != nil {
					mu.Lock()
					phase = "failed"
					message = "无法分配本地端口或打开日志，请检查目录权限。"
					mu.Unlock()
					continue
				}
				cmd := exec.Command(exe, "-addr", addr, "-proxy", proxy, "-data", filepath.Join(home, "data"))
				childToken := launcherToken()
				cmd.Env = append(launcherEnv(home, os.Environ()), "ARTEX_LAUNCH_TOKEN="+childToken, "ARTEX_LAUNCH_PARENT=1")
				parentRead, parentWrite, pipeErr := os.Pipe()
				if pipeErr != nil {
					log.Close()
					mu.Lock()
					phase = "failed"
					message = "无法创建后端监督通道，请重新启动。"
					mu.Unlock()
					continue
				}
				cmd.Stdin = parentRead
				cmd.Stdout = log
				cmd.Stderr = log
				if e = cmd.Start(); e != nil {
					parentRead.Close()
					parentWrite.Close()
					log.Close()
					mu.Lock()
					phase = "failed"
					message = "ARTEX 启动失败，请查看下方日志路径。"
					mu.Unlock()
					continue
				}
				parentRead.Close()
				done := make(chan error, 1)
				childFinished := make(chan struct{})
				go func() { err := cmd.Wait(); parentWrite.Close(); log.Close(); close(childFinished); done <- err }()
				go func() {
					select {
					case <-childFinished:
						return
					case <-ctx.Done():
						parentWrite.Close()
					}
					select {
					case <-childFinished:
					case <-time.After(5 * time.Second):
						_ = cmd.Process.Kill()
					}
				}()
				ready := false
				for i := 0; i < 100; i++ {
					select {
					case e := <-done:
						mu.Lock()
						phase = "failed"
						message = "ARTEX 在启动过程中退出，请查看日志检查数据库连接或端口占用。"
						mu.Unlock()
						if x, ok := e.(*exec.ExitError); ok && x.ExitCode() == 75 {
							mu.Lock()
							phase = "starting"
							mu.Unlock()
							start <- struct{}{}
						}
						goto finished
					case <-ctx.Done():
						<-done
						return
					case <-time.After(100 * time.Millisecond):
					}
					// Health is probed only on the newly launched child's selected port; the child must still be alive.
					resp, e := (&http.Client{Timeout: 200 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Get("http://" + addr + "/api/health")
					if e == nil {
						resp.Body.Close()
						if resp.StatusCode == 200 && resp.Header.Get("X-ARTEX-Launch-Token") == childToken {
							ready = true
							break
						}
					}
				}
				if ready {
					mu.Lock()
					ui = "http://" + addr
					phase = "running"
					message = "ARTEX 已启动，可以打开应用。"
					mu.Unlock()
					launcherOpen("http://" + addr)
				} else {
					mu.Lock()
					phase = "failed"
					message = "ARTEX 启动超时，请查看日志检查数据库连接。"
					mu.Unlock()
					cmd.Process.Kill()
				}
				e = <-done
				mu.Lock()
				ui = "http://" + s.Address + "/"
				phase = "failed"
				message = "ARTEX 已退出，请查看日志，然后停止启动器并重新启动。"
				mu.Unlock()
				if x, ok := e.(*exec.ExitError); ok && x.ExitCode() == 75 {
					mu.Lock()
					phase = "starting"
					mu.Unlock()
					start <- struct{}{}
				}
			finished:
			}
		}
	}()
	<-ctx.Done()
	<-workerDone
	shutdown, c := context.WithTimeout(context.Background(), 2*time.Second)
	defer c()
	srv.Shutdown(shutdown)
	return 0
}

func launcherPing(ctx context.Context, dsn string) error {
	if dsn == "" {
		return errors.New("empty connection")
	}
	ctx, c := context.WithTimeout(ctx, 5*time.Second)
	defer c()
	conn, e := pgx.Connect(ctx, dsn)
	if e != nil {
		return e
	}
	defer conn.Close(ctx)
	return conn.Ping(ctx)
}

func launcherDSN(f launcherForm, password string) (string, error) {
	host := strings.Trim(f.Host, "[]")
	port, e := strconv.Atoi(f.Port)
	if e != nil || port < 1 || port > 65535 || host == "" || strings.ContainsAny(host, "/\\@?#") || f.User == "" || f.Database == "" {
		return "", errors.New("请填写有效的主机、端口、用户名和数据库名称。")
	}
	local := strings.EqualFold(host, "localhost")
	if ip := net.ParseIP(host); ip != nil {
		local = ip.IsLoopback()
	}
	tls := f.TLS
	if tls == "auto" || tls == "" {
		tls = "verify-full"
		if local {
			tls = "disable"
		}
	}
	if tls != "verify-full" && tls != "disable" || tls == "disable" && !local {
		return "", errors.New("远程数据库须使用 TLS 验证；本地数据库可关闭 TLS。")
	}
	u := url.URL{Scheme: "postgres", Host: net.JoinHostPort(host, strconv.Itoa(port)), User: url.UserPassword(f.User, password), Path: "/" + f.Database, RawPath: "/" + url.PathEscape(f.Database)}
	q := url.Values{}
	q.Set("sslmode", tls)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func launcherWriteConfig(path, dsn string) error {
	// Link publishes a complete private file atomically and fails if the destination exists.
	if e := os.MkdirAll(filepath.Dir(path), 0700); e != nil {
		return e
	}
	f, e := os.CreateTemp(filepath.Dir(path), ".artex-config-")
	if e != nil {
		return e
	}
	defer os.Remove(f.Name())
	if e = f.Chmod(0600); e == nil {
		e = json.NewEncoder(f).Encode(config.Config{Database: config.Database{DSN: dsn}})
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return e
	}
	if ce != nil {
		return ce
	}
	return os.Link(f.Name(), path)
}
func launcherSeedSkills(src, dst string) error {
	if _, e := os.Stat(src); os.IsNotExist(e) {
		return nil
	}
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, e := filepath.Rel(src, path)
		if e != nil {
			return e
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			if st, e := os.Lstat(target); e == nil && st.Mode()&os.ModeSymlink != 0 {
				return filepath.SkipDir
			}
			return os.MkdirAll(target, 0700)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		defer in.Close()
		out, e := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if os.IsExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		_, e = io.Copy(out, in)
		if e == nil {
			if stat, err := in.Stat(); err != nil {
				e = err
			} else {
				e = out.Chmod(0600 | stat.Mode()&0100)
			}
		}
		ce := out.Close()
		if e != nil {
			return e
		}
		return ce
	})
}

func launcherSourceSkills(exe string) string {
	candidates := []string{filepath.Join(filepath.Dir(exe), "skills"), filepath.Join(filepath.Dir(exe), "..", "Resources", "skills")}
	if runtime.GOOS == "linux" {
		candidates = append(candidates, "/usr/share/artex/skills")
	}
	for _, p := range candidates {
		if st, e := os.Stat(p); e == nil && st.IsDir() {
			return p
		}
	}
	return candidates[0]
}
