package traffic

import (
	"bufio"
	"crypto/tls"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

func TestSOCKS5AuthenticatedHTTPAndHTTPS(t *testing.T) {
	for _, secure := range []bool{false, true} {
		t.Run(fmt.Sprintf("TLS=%v", secure), func(t *testing.T) {
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "via socks") })
			target := httptest.NewUnstartedServer(handler)
			if secure {
				target.StartTLS()
			} else {
				target.Start()
			}
			defer target.Close()
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
				c.SetDeadline(time.Now().Add(5 * time.Second))
				r := bufio.NewReader(c)
				read := func(n int) ([]byte, error) { b := make([]byte, n); _, err := io.ReadFull(r, b); return b, err }
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
				if string(user) != "fixture" || string(pass) != "password" {
					done <- fmt.Errorf("credentials lost")
					return
				}
				c.Write([]byte{1, 0})
				h, err = read(4)
				if err != nil {
					done <- err
					return
				}
				if h[3] != 1 {
					done <- fmt.Errorf("expected IPv4")
					return
				}
				dst, err := read(6)
				if err != nil {
					done <- err
					return
				}
				address := net.JoinHostPort(net.IP(dst[:4]).String(), fmt.Sprint(binary.BigEndian.Uint16(dst[4:])))
				if address != target.Listener.Addr().String() {
					done <- fmt.Errorf("unexpected route %s", address)
					return
				}
				up, err := net.DialTimeout("tcp", address, time.Second)
				if err != nil {
					done <- err
					return
				}
				defer up.Close()
				up.SetDeadline(time.Now().Add(5 * time.Second))
				c.Write([]byte{5, 0, 0, 1, 127, 0, 0, 1, 0, 1})
				go io.Copy(up, r)
				io.Copy(c, up)
				done <- nil
			}()
			tr, _ := startLoopbackTraffic(t)
			if err := tr.SetUpstreamProxy("socks5://fixture:password@" + ln.Addr().String()); err != nil {
				t.Fatal(err)
			}
			u, _ := url.Parse(tr.ProxyAddr())
			transport := &http.Transport{Proxy: http.ProxyURL(u), TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 4 * time.Second}
			resp, err := client.Get(target.URL)
			if err != nil {
				t.Fatal(err)
			}
			b, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil || string(b) != "via socks" {
				t.Fatalf("body=%q err=%v", b, err)
			}
			transport.CloseIdleConnections()
			tr.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSOCKS5RefusalReturnsGatewayError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "direct") }))
	defer target.Close()
	tr, _ := startLoopbackTraffic(t)
	if err := tr.SetUpstreamProxy("socks5://" + addr); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(tr.ProxyAddr())
	transport := &http.Transport{Proxy: http.ProxyURL(u)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get(target.URL)
	if err != nil {
		t.Fatalf("expected ordinary gateway response, got %v", err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if err := tr.SetUpstreamProxy(""); err != nil {
		t.Fatal(err)
	}
	resp, err = client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil || string(b) != "direct" {
		t.Fatalf("direct body=%q err=%v", b, err)
	}
}
