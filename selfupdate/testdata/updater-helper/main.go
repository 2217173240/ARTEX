package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"

	"github.com/Autumn-27/artex/selfupdate"
)

var version = "development"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "-h":
			p, err := selfupdate.ResolvePaths()
			if err != nil {
				return err
			}
			if _, err := os.Stat(filepath.Join(p.Dir, "smoke-error")); err == nil {
				return fmt.Errorf("native help failure")
			} else if !os.IsNotExist(err) {
				return err
			}
			b, err := json.Marshal(os.Environ())
			if err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(p.Dir, "smoke-environment.json"), b, 0o600)
		case "paths":
			p, err := selfupdate.ResolvePaths()
			if err != nil {
				return err
			}
			return json.NewEncoder(os.Stdout).Encode(p)
		case "stage":
			b, err := os.ReadFile(os.Args[2])
			if err != nil {
				return err
			}
			var rel selfupdate.Release
			if err := json.Unmarshal(b, &rel); err != nil {
				return err
			}
			cert, err := os.ReadFile(os.Args[3])
			if err != nil {
				return err
			}
			roots := x509.NewCertPool()
			if !roots.AppendCertsFromPEM(cert) {
				return fmt.Errorf("读取本地 TLS 证书失败")
			}
			c, err := selfupdate.NewClient("")
			if err != nil {
				return err
			}
			defer c.CloseIdleConnections()
			tr := c.Transport.(*http.Transport)
			tr.TLSClientConfig = &tls.Config{RootCAs: roots, ServerName: "example.com", MinVersion: tls.VersionTLS12}
			tr.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, os.Args[4])
			}
			if err := selfupdate.Stage(context.Background(), c, &rel, version, nil); err != nil {
				return err
			}
			os.Exit(selfupdate.ExitRestart)
		case "rollback":
			if err := selfupdate.Rollback(); err != nil {
				return err
			}
			os.Exit(selfupdate.ExitRestart)
		}
	}
	action, state := selfupdate.Bootstrap()
	if action == selfupdate.Restart {
		os.Exit(selfupdate.ExitRestart)
	}
	if len(os.Args) > 1 && os.Args[1] == "settle" {
		selfupdate.Settle()
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Version string
		State   selfupdate.State
	}{version, state})
}
