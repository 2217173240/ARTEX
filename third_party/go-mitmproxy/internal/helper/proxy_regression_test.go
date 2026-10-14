package helper

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestHTTPAndHTTPSConnect(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(map[bool]string{false: "HTTP", true: "HTTPS"}[secure], func(t *testing.T) {
			errs := make(chan error, 1)
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "CONNECT" || r.Host != "127.0.0.1:80" || r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
					errs <- errors.New("CONNECT routing or credentials changed")
					w.WriteHeader(400)
					return
				}
				c, _, err := w.(http.Hijacker).Hijack()
				if err != nil {
					errs <- err
					return
				}
				defer c.Close()
				_, err = io.WriteString(c, "HTTP/1.1 200 Connection established\r\n\r\nok")
				errs <- err
			}))
			if secure {
				server.StartTLS()
			} else {
				server.Start()
			}
			defer server.Close()
			u, _ := url.Parse(server.URL)
			u.User = url.UserPassword("user", "pass")
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := GetProxyConn(ctx, u, "127.0.0.1:80", true)
			if err != nil {
				t.Fatal(err)
			}
			defer c.Close()
			if err := <-errs; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSOCKS5CanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	u, _ := url.Parse("socks5://127.0.0.1:1")
	conn, err := GetProxyConn(ctx, u, "127.0.0.1:80", false)
	if conn != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("conn=%v err=%v", conn, err)
	}
}

func TestSOCKS5Handshake(t *testing.T) {
	for _, reject := range []bool{false, true} {
		t.Run(map[bool]string{false: "authenticated", true: "rejected"}[reject], func(t *testing.T) {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer ln.Close()
			done := make(chan error, 1)
			go func() {
				c, err := ln.Accept()
				if err != nil {
					done <- err
					return
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(c, b); return b, err }
				h, err := read(2)
				if err != nil {
					done <- err
					return
				}
				if _, err = read(int(h[1])); err != nil {
					done <- err
					return
				}
				c.Write([]byte{5, 2})
				h, err = read(2)
				if err != nil {
					done <- err
					return
				}
				user, err := read(int(h[1]))
				if err != nil {
					done <- err
					return
				}
				h, err = read(1)
				if err != nil {
					done <- err
					return
				}
				pass, err := read(int(h[0]))
				if err != nil {
					done <- err
					return
				}
				if string(user) != "user" || string(pass) != "pass" {
					done <- errors.New("credentials lost")
					return
				}
				if reject {
					c.Write([]byte{1, 1})
					done <- nil
					return
				}
				c.Write([]byte{1, 0})
				h, err = read(4)
				if err != nil {
					done <- err
					return
				}
				if h[3] != 1 {
					done <- errors.New("expected IPv4 target")
					return
				}
				if _, err = read(6); err != nil {
					done <- err
					return
				}
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 80})
				c.Write([]byte("ok"))
				done <- nil
			}()
			u, _ := url.Parse("socks5://user:pass@" + ln.Addr().String())
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			c, err := GetProxyConn(ctx, u, "127.0.0.1:80", false)
			if reject {
				if c != nil || err == nil {
					t.Fatalf("conn=%v err=%v", c, err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				defer c.Close()
				b := make([]byte, 2)
				if _, err := io.ReadFull(c, b); err != nil || string(b) != "ok" {
					t.Fatalf("payload=%q err=%v", b, err)
				}
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}
