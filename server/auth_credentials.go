package server

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"runtime"

	"github.com/Autumn-27/artex/db"
	"github.com/golang-jwt/jwt/v5"
)

const setupTokenFilename = "setup.token"

// Both credentials derive from the private base key with distinct domains.
// Binding admin sessions to the stored hash invalidates all older sessions on
// the next request after a password change, including an external DB reset.
func adminSigningKey(base []byte, passwordHash string) []byte {
	m := hmac.New(sha256.New, base)
	m.Write([]byte("artex/admin-session/v1\x00"))
	m.Write([]byte(passwordHash))
	return m.Sum(nil)
}

func deriveSetupToken(base []byte) string {
	m := hmac.New(sha256.New, base)
	m.Write([]byte("artex/bootstrap-ownership/v1"))
	return hex.EncodeToString(m.Sum(nil))
}

// ensureSetupTokenFile publishes a complete private delivery copy atomically.
// A pre-existing unrelated file, permissive Unix file, directory or symlink is an
// error and is never replaced or chmodded. The copy is not the trust source.
func ensureSetupTokenFile(keyDir, token string) error {
	path := filepath.Join(keyDir, setupTokenFilename)
	checkExisting := func() error {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		// Windows reports writable files as 0666 and inherits the private key
		// directory's ACL; Unix enforces the owner-only mode directly.
		if !info.Mode().IsRegular() || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
			return errors.New("setup.token must be a private regular file (0600)")
		}
		f, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open setup.token: %w", err)
		}
		defer f.Close()
		opened, err := f.Stat()
		if err != nil || !os.SameFile(info, opened) {
			return errors.New("setup.token changed during access")
		}
		// A bounded read also rejects unrelated large files without loading them.
		buf, err := io.ReadAll(io.LimitReader(f, int64(len(token)+2)))
		if err != nil || (string(buf) != token && string(buf) != token+"\n") {
			return errors.New("setup.token does not match this instance; existing file preserved")
		}
		return nil
	}
	if _, err := os.Lstat(path); err == nil {
		return checkExisting()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("inspect setup.token: %w", err)
	}
	created, err := publishPrivateAuthFile(keyDir, setupTokenFilename, []byte(token+"\n"))
	if err != nil {
		return err
	}
	if !created {
		return checkExisting()
	}
	return nil
}

// publishPrivateAuthFile exposes a fully written 0600 file without replacing
// any existing path. Losing concurrent creators retain the winning file.
func publishPrivateAuthFile(dir, name string, content []byte) (bool, error) {
	f, err := os.CreateTemp(dir, ".auth-private-*")
	if err != nil {
		return false, fmt.Errorf("create %s: %w", name, err)
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(content); err != nil {
		f.Close()
		return false, fmt.Errorf("write %s: %w", name, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return false, fmt.Errorf("sync %s: %w", name, err)
	}
	if err := f.Close(); err != nil {
		return false, fmt.Errorf("close %s: %w", name, err)
	}
	if err := os.Link(f.Name(), filepath.Join(dir, name)); err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("publish %s: %w", name, err)
	}
	return true, nil
}

func (s *Server) prepareAuthSetup(keyDir string) error {
	if s.m == nil || s.m.pg == nil || len(s.jwtKey) < 32 {
		return errors.New("authentication storage or private key unavailable")
	}
	hash, _, err := s.m.pg.GetSetting(authPassKey)
	if err != nil {
		return errors.New(errDataSourceUnavailable)
	}
	if hash != "" {
		return nil
	}
	token := deriveSetupToken(s.jwtKey)
	if err := ensureSetupTokenFile(keyDir, token); err != nil {
		return err
	}
	s.setupToken = token
	log.Printf("[auth] 首次设置需要本机 setup token；请读取 %s 并填入设置页面", filepath.Join(keyDir, setupTokenFilename))
	return nil
}

func (s *Server) signAdminJWT(hash string) (string, error) {
	if len(s.jwtKey) < 32 || hash == "" {
		return "", errors.New("admin credentials unavailable")
	}
	return signJWT(adminSigningKey(s.jwtKey, hash))
}

func (s *Server) verifyAdminJWT(token, hash string) bool {
	if len(s.jwtKey) < 32 || hash == "" || token == "" {
		return false
	}
	_, err := jwt.ParseWithClaims(token, &jwt.RegisteredClaims{}, func(*jwt.Token) (any, error) {
		return adminSigningKey(s.jwtKey, hash), nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithExpirationRequired(), jwt.WithSubject("ARTEX"))
	return err == nil
}

// authenticatedAdminHash reads the current credential for every protected
// request. There is deliberately no cache that could accept a revoked session.
func (s *Server) authenticatedAdminHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	pg := s.authDatabase(w)
	if pg == nil {
		return "", false
	}
	hash, _, err := pg.GetSetting(authPassKey)
	if err != nil {
		writePublicErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return "", false
	}
	if !s.verifyAdminJWT(extractToken(r), hash) {
		writeErr(w, http.StatusUnauthorized, "未授权或会话已过期")
		return "", false
	}
	return hash, true
}

func (s *Server) authDatabase(w http.ResponseWriter) *db.DB {
	if s.m == nil || s.m.pg == nil {
		writePublicErr(w, http.StatusServiceUnavailable, errDataSourceUnavailable)
		return nil
	}
	return s.m.pg
}
