package server

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

type authBodyDeadlineWriter struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
	err       error
	clearErr  error
}

func (w *authBodyDeadlineWriter) SetReadDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	if deadline.IsZero() {
		return w.clearErr
	}
	return w.err
}

type authBodyUnwrapWriter struct{ http.ResponseWriter }

func (w authBodyUnwrapWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func TestAuthBodyDecodeBoundaries(t *testing.T) {
	const credential = "fixture-secret-never-echo"
	base := `{"password":"` + credential + `"}`
	exact := base + strings.Repeat(" ", maxAuthBodyBytes-len(base))
	for _, tc := range []struct {
		name, body string
		want       int
	}{
		{"ordinary", base, 204},
		{"unknown fields compatible", `{"password":"` + credential + `","future_option":true}`, 204},
		{"escaped unicode", `{"password":"` + strings.Repeat(`\u5bc6`, 24) + `"}`, 204},
		{"exact byte limit", exact, 204},
		{"over byte limit", exact + " ", 413},
		{"leading whitespace over limit", strings.Repeat(" ", maxAuthBodyBytes) + base, 413},
		{"unknown field over limit", `{"padding":"` + strings.Repeat("x", 2*maxAuthBodyBytes) + `"}`, 413},
		{"trailing object", base + `{}`, 400},
		{"trailing null", base + `null`, 400},
		{"trailing garbage", base + credential, 400},
		{"malformed", `{"password":!` + credential, 400},
		{"truncated", `{"password":"` + credential, 400},
		{"empty", "", 400},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
			r := httptest.NewRequest("POST", "/", strings.NewReader(tc.body))
			// Exercise streaming enforcement, independently of Content-Length.
			r.ContentLength = -1
			body := newAuthBody(w, r, time.Second)
			var req struct {
				Password string `json:"password"`
			}
			if body.decode(&req) {
				w.WriteHeader(204)
			}
			body.finish()
			if w.Code != tc.want {
				t.Fatalf("status=%d want=%d", w.Code, tc.want)
			}
			if strings.Contains(w.Body.String(), credential) {
				t.Fatal("response exposed submitted credentials")
			}
			if len(w.deadlines) != 2 || w.deadlines[0].IsZero() {
				t.Fatal("missing initial read deadline or completion decision")
			}
			if tc.want == 204 {
				if !w.deadlines[1].IsZero() || r.Close || w.Header().Get("Connection") != "" {
					t.Fatal("complete request did not restore reusable connection")
				}
				if tc.name == "escaped unicode" && (len(req.Password) != 72 || validatePassword(req.Password) != "") {
					t.Fatal("escaped 72-byte password changed")
				}
			} else if w.deadlines[1].IsZero() || !r.Close || w.Header().Get("Connection") != "close" {
				t.Fatal("invalid request did not retain deadline and close connection")
			}
		})
	}
}

type authBodyCountReader struct {
	io.Reader
	read, closes int
}

func (r *authBodyCountReader) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
func (r *authBodyCountReader) Close() error { r.closes++; return nil }

func TestAuthBodyReadBudgetAndWriterSupport(t *testing.T) {
	t.Run("controller unwraps middleware", func(t *testing.T) {
		inner := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		body := newAuthBody(authBodyUnwrapWriter{inner}, httptest.NewRequest("POST", "/", strings.NewReader(`{}`)), authBodyReadTimeout)
		if !body.decode(&struct{}{}) {
			t.Fatal("unwrapped writer rejected")
		}
		body.finish()
		if len(inner.deadlines) != 2 || time.Until(inner.deadlines[0]) < authBodyReadTimeout-time.Second || !inner.deadlines[1].IsZero() {
			t.Fatal("middleware did not receive production deadline and clear")
		}
	})
	t.Run("bounded read without draining close", func(t *testing.T) {
		r := httptest.NewRequest("POST", "/", nil)
		counter := &authBodyCountReader{Reader: strings.NewReader(`{"padding":"` + strings.Repeat("x", 1<<20) + `"}`)}
		r.Body, r.ContentLength = counter, -1
		w := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		body := newAuthBody(w, r, time.Second)
		if body.decode(&struct{}{}) {
			t.Fatal("oversized body accepted")
		}
		body.finish()
		if counter.read > maxAuthBodyBytes+1 || counter.closes != 0 {
			t.Fatalf("read=%d closes=%d", counter.read, counter.closes)
		}
	})
	t.Run("unsupported nonserver writer", func(t *testing.T) {
		w := httptest.NewRecorder()
		if err := http.NewResponseController(w).SetReadDeadline(time.Now()); !errors.Is(err, http.ErrNotSupported) {
			t.Fatalf("Recorder unexpectedly supports deadlines: %v", err)
		}
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		body := newAuthBody(w, r, time.Second)
		if !body.decode(&struct{}{}) {
			t.Fatal("finite nonserver request rejected")
		}
		body.finish()
	})
	t.Run("deadline failure is fail closed", func(t *testing.T) {
		w := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), err: errors.New("fixture failure")}
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		body := newAuthBody(w, r, time.Second)
		if body.decode(&struct{}{}) || w.Code != 503 {
			t.Fatal("deadline setup error accepted")
		}
		body.finish()
	})
	t.Run("deadline clear failure is fail closed", func(t *testing.T) {
		w := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder(), clearErr: errors.New("fixture clear failure")}
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		body := newAuthBody(w, r, time.Second)
		if body.decode(&struct{}{}) || w.Code != 503 || body.complete {
			t.Fatal("deadline clear failure accepted")
		}
		body.finish()
		if !r.Close || len(w.deadlines) != 3 || w.deadlines[2].IsZero() || w.Header().Get("Connection") != "close" {
			t.Fatal("deadline clear failure did not close")
		}
	})
	t.Run("preserve outer close", func(t *testing.T) {
		w := &authBodyDeadlineWriter{ResponseRecorder: httptest.NewRecorder()}
		w.Header().Set("Connection", "close")
		r := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
		r.Close = true
		body := newAuthBody(w, r, time.Second)
		if !body.decode(&struct{}{}) {
			t.Fatal("complete request rejected")
		}
		body.finish()
		if !r.Close || w.Header().Get("Connection") != "close" {
			t.Fatal("outer close preference removed")
		}
	})
}

func authBodyTestHandler(timeout time.Duration) http.Handler {
	return cors(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			w.WriteHeader(204)
			return
		}
		body := newAuthBody(w, r, timeout)
		defer body.finish()
		if r.URL.Path == "/early" {
			writeErr(w, 403, "请求来源不允许")
			return
		}
		if body.decode(&struct{}{}) {
			w.WriteHeader(204)
		}
	}))
}

func TestAuthBodyHTTP1SlowAndIncomplete(t *testing.T) {
	for _, tc := range []struct {
		name, headers, data string
		want                int
	}{
		{"partial fixed length", "Content-Length: 100\r\n", "{", 408},
		{"JSON complete but fixed length unfinished", "Content-Length: 100\r\n", `{}`, 408},
		{"chunked awaiting EOF", "Transfer-Encoding: chunked\r\n", "2\r\n{}\r\n", 408},
		{"truncated fixed length", "Content-Length: 100\r\n", "{", 400},
		{"malformed unread remainder", "Content-Length: 100\r\n", "!", 400},
		{"oversized declared unread body", "Content-Length: 5000\r\n", "", 413},
	} {
		t.Run(tc.name, func(t *testing.T) {
			closed := make(chan struct{}, 1)
			server := httptest.NewUnstartedServer(authBodyTestHandler(80 * time.Millisecond))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				if state == http.StateClosed {
					select {
					case closed <- struct{}{}:
					default:
					}
				}
			}
			server.Start()
			defer server.Close()
			conn, err := net.Dial("tcp", server.Listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			conn.SetDeadline(time.Now().Add(2 * time.Second))
			fmt.Fprintf(conn, "POST / HTTP/1.1\r\nHost: localhost\r\n%s\r\n%s", tc.headers, tc.data)
			if tc.name == "truncated fixed length" {
				if err := conn.(*net.TCPConn).CloseWrite(); err != nil {
					t.Fatal(err)
				}
			}
			response, err := http.ReadResponse(bufio.NewReader(conn), nil)
			if err != nil {
				t.Fatalf("no bounded response: %v", err)
			}
			defer response.Body.Close()
			if response.StatusCode != tc.want || !response.Close {
				t.Fatalf("status=%d close=%v", response.StatusCode, response.Close)
			}
			if _, err := io.ReadAll(response.Body); err != nil {
				t.Fatal(err)
			}
			select {
			case <-closed:
			case <-time.After(time.Second):
				t.Fatal("server cleanup waited on unread body")
			}
		})
	}
}

func TestAuthBodyHTTP1EarlyRejectionsCloseUnreadBody(t *testing.T) {
	for _, action := range []string{"init", "login", "change-password"} {
		for _, reason := range []string{"database unavailable", "already initialized or unauthenticated", "rate limited", "CSRF", "Basic gate"} {
			if (reason == "rate limited" && action == "change-password") || (reason == "already initialized or unauthenticated" && action == "login") {
				continue
			}
			t.Run(action+"/"+reason, func(t *testing.T) {
				s, store := newAuthMemoryServer(t)
				want := 503
				switch reason {
				case "database unavailable":
					store.err = errors.New("offline")
					if action == "login" {
						s.m = nil
					}
				case "already initialized or unauthenticated":
					store.values[authPassKey] = "initialized"
					want = 401
					if action == "init" {
						want = 403
					}
				case "rate limited":
					for range authAttemptLimit {
						s.allowAuthAttempt(httptest.NewRecorder(), httptest.NewRequest("POST", "/", nil))
					}
					want = 429
				case "CSRF":
					want = 403
				case "Basic gate":
					s.basicAuth.cfg = basicAuthConfig{Enabled: true, Username: "gate", PasswordHash: "fixture-hash"}
					want = 401
				}
				handler := http.HandlerFunc(s.authLogin)
				if action == "init" {
					handler = s.authInit
				}
				if action == "change-password" {
					handler = s.authChangePassword
				}
				if reason == "rate limited" {
					// Use an exhausted fixture peer without trusting forwarded headers.
					inner := handler
					handler = func(w http.ResponseWriter, r *http.Request) { r.RemoteAddr = "192.0.2.1:1234"; inner(w, r) }
				}
				closed := make(chan struct{}, 1)
				server := httptest.NewUnstartedServer(s.requireBasicAuth(s.requireAuth(handler)))
				server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
					if state == http.StateClosed {
						select {
						case closed <- struct{}{}:
						default:
						}
					}
				}
				server.Start()
				defer server.Close()
				conn, err := net.Dial("tcp", server.Listener.Addr().String())
				if err != nil {
					t.Fatal(err)
				}
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(time.Second))
				origin := ""
				if reason == "CSRF" {
					origin = "Origin: https://attacker.test\r\n"
				}
				fmt.Fprintf(conn, "POST /api/auth/%s HTTP/1.1\r\nHost: localhost\r\n%sContent-Length: 100\r\n\r\n", action, origin)
				response, err := http.ReadResponse(bufio.NewReader(conn), nil)
				if err != nil {
					t.Fatalf("early rejection drained slow body: %v", err)
				}
				defer response.Body.Close()
				if response.StatusCode != want || !response.Close {
					t.Fatalf("status=%d want=%d close=%v", response.StatusCode, want, response.Close)
				}
				io.Copy(io.Discard, response.Body)
				select {
				case <-closed:
				case <-time.After(time.Second):
					t.Fatal("early rejection did not finish connection cleanup")
				}
			})
		}
	}
}

func TestAuthBodyRoutesAcceptEscapedPasswordLimits(t *testing.T) {
	s, store := newAuthMemoryServer(t)
	unicodeJSON := strings.Repeat(`\u5bc6`, 24)
	password := strings.Repeat("密", 24)
	w := authCredentialRequest(s, "init", `{"password":"`+unicodeJSON+`","setup_token":"`+s.setupToken+`","future_option":true}`, "", nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("escaped initialization status=%d", w.Code)
	}
	w = authCredentialRequest(s, "login", `{"username":"ARTEX","password":"`+unicodeJSON+`"}`, "", nil)
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("escaped login status=%d", w.Code)
	}
	if bcrypt.CompareHashAndPassword([]byte(store.values[authPassKey]), []byte(password)) != nil {
		t.Fatal("escaped Unicode password changed during initialization")
	}
	w = authCredentialRequest(s, "change-password", `{"old_password":"`+unicodeJSON+`","new_password":"`+strings.Repeat(`\u0061`, 72)+`"}`, "", w.Result().Cookies()[0])
	if w.Code != 200 || bcrypt.CompareHashAndPassword([]byte(store.values[authPassKey]), []byte(strings.Repeat("a", 72))) != nil {
		t.Fatalf("escaped 72-byte password change status=%d", w.Code)
	}
}

func TestAuthBodyHTTPKeepAliveAndHTTP2(t *testing.T) {
	for _, http2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("HTTP2=%v", http2), func(t *testing.T) {
			const timeout = 80 * time.Millisecond
			server := httptest.NewUnstartedServer(authBodyTestHandler(timeout))
			server.EnableHTTP2 = http2
			if http2 {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			client := server.Client()
			client.Timeout = 2 * time.Second
			post := func(body string) *http.Response {
				t.Helper()
				// Force an unknown length: HTTP/1 uses chunked transfer and HTTP/2
				// must see END_STREAM. This cannot pass via the declared-size check.
				r, err := client.Post(server.URL, "application/json", io.NopCloser(strings.NewReader(body)))
				if err != nil {
					t.Fatal(err)
				}
				if http2 && r.ProtoMajor != 2 {
					t.Fatal("HTTP/2 was not negotiated")
				}
				io.Copy(io.Discard, r.Body)
				r.Body.Close()
				return r
			}
			if r := post(`{}`); r.StatusCode != 204 || r.Close {
				t.Fatal("valid request did not preserve keepalive")
			}
			// The next unrelated request must survive past the credential deadline.
			time.Sleep(2 * timeout)
			var reused atomic.Bool
			req, _ := http.NewRequest("GET", server.URL, nil)
			req = req.WithContext(httptrace.WithClientTrace(req.Context(), &httptrace.ClientTrace{GotConn: func(info httptrace.GotConnInfo) { reused.Store(info.Reused) }}))
			r, err := client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			r.Body.Close()
			if r.StatusCode != 204 || !reused.Load() {
				t.Fatal("successful body lost reusable connection")
			}
			for _, tc := range []struct {
				body string
				want int
			}{{`{}` + strings.Repeat(" ", maxAuthBodyBytes-2), 204}, {`{}` + strings.Repeat(" ", maxAuthBodyBytes-1), 413}, {`{}{}`, 400}} {
				if r := post(tc.body); r.StatusCode != tc.want {
					t.Fatalf("status=%d want=%d", r.StatusCode, tc.want)
				} else if http2 && r.Header.Get("Connection") != "" {
					t.Fatal("illegal HTTP/2 Connection header")
				}
			}
			if !http2 {
				return
			}
			// A stalled HTTP/2 stream times out without killing other streams.
			reader, writer := io.Pipe()
			defer reader.Close()
			defer writer.Close()
			req, _ = http.NewRequest("POST", server.URL, reader)
			responseCh := make(chan *http.Response, 1)
			errorCh := make(chan error, 1)
			go func() {
				response, err := client.Do(req)
				if err != nil {
					errorCh <- err
				} else {
					responseCh <- response
				}
			}()
			if _, err := io.WriteString(writer, "{"); err != nil {
				t.Fatal(err)
			}
			select {
			case response := <-responseCh:
				if response.StatusCode != 408 {
					t.Fatalf("HTTP/2 slow status=%d", response.StatusCode)
				}
				io.Copy(io.Discard, response.Body)
				response.Body.Close()
			case err := <-errorCh:
				t.Fatalf("HTTP/2 stream did not return timeout response: %v", err)
			case <-time.After(time.Second):
				t.Fatal("HTTP/2 stream did not time out")
			}
			if r := post(`{}`); r.StatusCode != 204 {
				t.Fatal("HTTP/2 connection unusable after failed stream")
			}
			// Early rejection must close an unread stream without waiting for its
			// configured body deadline, while preserving other HTTP/2 streams.
			earlyReader, earlyWriter := io.Pipe()
			defer earlyReader.Close()
			defer earlyWriter.Close()
			earlyRequest, _ := http.NewRequest("POST", server.URL+"/early", earlyReader)
			earlyResponse, err := client.Do(earlyRequest)
			if err != nil {
				t.Fatal(err)
			}
			if earlyResponse.StatusCode != 403 || earlyResponse.Header.Get("Connection") != "" {
				t.Fatal("HTTP/2 early rejection failed")
			}
			io.Copy(io.Discard, earlyResponse.Body)
			earlyResponse.Body.Close()
			if r := post(`{}`); r.StatusCode != 204 {
				t.Fatal("HTTP/2 connection unusable after unread early rejection")
			}
		})
	}
}

func TestAuthBodyRoutesRejectInvalidEnvelope(t *testing.T) {
	for _, action := range []string{"init", "login", "change-password"} {
		for _, envelope := range []string{"trailing JSON", "unknown field over limit", "whitespace over limit"} {
			t.Run(action+"/"+envelope, func(t *testing.T) {
				s, store := newAuthMemoryServer(t)
				const password = "fixture-password"
				var bearer, originalHash string
				body := `{"password":"` + password + `","setup_token":"` + s.setupToken + `"}`
				if action != "init" {
					hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
					if err != nil {
						t.Fatal(err)
					}
					originalHash = string(hash)
					store.values[authPassKey] = originalHash
					bearer, err = s.signAdminJWT(originalHash)
					if err != nil {
						t.Fatal(err)
					}
					body = `{"username":"ARTEX","password":"` + password + `"}`
					if action == "change-password" {
						body = `{"old_password":"` + password + `","new_password":"replacement-password"}`
					}
				}
				want := 413
				switch envelope {
				case "trailing JSON":
					body += `{}`
					want = 400
				case "unknown field over limit":
					body = strings.TrimSuffix(body, "}") + `,"padding":"` + strings.Repeat("x", 5000) + `"}`
				case "whitespace over limit":
					body += strings.Repeat(" ", 5000)
				}
				w := authCredentialRequest(s, action, body, bearer, nil)
				if w.Code != want {
					t.Fatalf("status=%d want=%d", w.Code, want)
				}
				if store.values[authPassKey] != originalHash || len(w.Result().Cookies()) != 0 {
					t.Fatal("rejected body mutated credentials or issued a session")
				}
			})
		}
	}
}
