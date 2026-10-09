package traffic

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

func startLoopbackTraffic(t *testing.T) (*Traffic, <-chan error) {
	t.Helper()
	// Select an unused loopback port without relying on a fixed test port.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	tr, err := Open(t.TempDir(), addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Also close the dependency directly so the regression's failing version
		// does not leave its listener behind after the test reports the failure.
		_ = tr.proxy.Close()
		_ = tr.Close()
	})
	started := make(chan error, 1)
	go func() { started <- tr.Start() }()
	deadline := time.Now().Add(3 * time.Second)
	for {
		conn, err := net.DialTimeout("tcp", addr, 50*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return tr, started
		}
		select {
		case err := <-started:
			t.Fatalf("proxy stopped before listening: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("proxy did not listen on %s: %v", addr, err)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func assertConnectionClosed(t *testing.T, conn net.Conn) {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	if n, err := conn.Read(b[:]); n != 0 || err == nil {
		t.Fatalf("closed connection read=(%d, %v), want an error", n, err)
	} else if timeout, ok := err.(net.Error); ok && timeout.Timeout() {
		t.Fatalf("connection remained open after Close: %v", err)
	}
}

func TestCloseStopsProxyAndReleasesListener(t *testing.T) {
	tr, started := startLoopbackTraffic(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "loopback response")
	}))
	defer upstream.Close()
	proxyURL, err := url.Parse(tr.ProxyAddr())
	if err != nil {
		t.Fatal(err)
	}
	transport := &http.Transport{Proxy: http.ProxyURL(proxyURL)}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 3 * time.Second}
	resp, err := client.Get(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "loopback response" {
		t.Fatalf("proxy response=%q err=%v", body, err)
	}
	if count, err := tr.Count(); err != nil || count != 1 {
		t.Fatalf("recorded exchanges=%d err=%v, want 1", count, err)
	}
	idle, err := net.Dial("tcp", tr.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer idle.Close()

	var callers sync.WaitGroup
	closeErrors := make(chan error, 2)
	for range 2 {
		callers.Go(func() { closeErrors <- tr.Close() })
	}
	callers.Wait()
	close(closeErrors)
	for err := range closeErrors {
		if err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	select {
	case err := <-started:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start returned %v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start did not return after Close")
	}
	assertConnectionClosed(t, idle)
	listener, err := net.Listen("tcp", tr.addr)
	if err != nil {
		t.Fatalf("listener address was not released: %v", err)
	}
	_ = listener.Close()
}

func TestCloseStopsHijackedProxyTunnel(t *testing.T) {
	tr, _ := startLoopbackTraffic(t)
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	upstreamClosed := make(chan error, 1)
	go func() {
		conn, err := upstream.Accept()
		if err != nil {
			upstreamClosed <- err
			return
		}
		defer conn.Close()
		_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		_, err = io.Copy(io.Discard, conn)
		upstreamClosed <- err
	}()
	// Use the existing passthrough path to establish a raw CONNECT tunnel to
	// the fake loopback server, which http.Server.Close cannot manage itself.
	tr.pass.Store("127.0.0.1", struct{}{})
	conn, err := net.Dial("tcp", tr.addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n\r\n", upstream.Addr(), upstream.Addr()); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("CONNECT status=%d, want 200", resp.StatusCode)
	}
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	assertConnectionClosed(t, conn)
	select {
	case err := <-upstreamClosed:
		if err != nil {
			t.Fatalf("upstream tunnel did not close cleanly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("upstream tunnel remained open after Close")
	}
}

func TestStartAfterCloseDoesNotListen(t *testing.T) {
	tr, _ := openTraffic(t)
	t.Cleanup(func() { _ = tr.proxy.Close() })
	if err := tr.Close(); err != nil {
		t.Fatal(err)
	}
	started := make(chan error, 1)
	go func() { started <- tr.Start() }()
	select {
	case err := <-started:
		if !errors.Is(err, http.ErrServerClosed) {
			t.Fatalf("Start after Close=%v, want http.ErrServerClosed", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Start remained active after Close")
	}
}
