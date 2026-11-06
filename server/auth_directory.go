package server

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// cleanupMatchingLegacyKeys retries removal only for confirmed copies of the winning key.
func cleanupMatchingLegacyKeys(primary string, key []byte, dirs []string) error {
	primaryInfo, err := os.Stat(primary)
	if err != nil {
		return fmt.Errorf("stat primary JWT key: %w", err)
	}
	canonical, err := canonicalAuthPath(primary)
	if err != nil {
		return err
	}
	seen := map[string]bool{canonical: true}
	for _, dir := range dirs {
		path := filepath.Join(dir, jwtKeyFilename)
		canonical, err := canonicalAuthPath(path)
		if err != nil || seen[canonical] {
			continue
		}
		seen[canonical] = true
		candidateInfo, err := os.Stat(path)
		if err != nil || os.SameFile(primaryInfo, candidateInfo) {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(bytes.TrimSpace(data), key) {
			continue
		}
		currentPrimary, err := os.Stat(primary)
		if err != nil {
			return fmt.Errorf("stat primary JWT key before cleanup: %w", err)
		}
		currentCandidate, err := os.Stat(path)
		if err != nil || !os.SameFile(candidateInfo, currentCandidate) || os.SameFile(currentPrimary, currentCandidate) {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove matching legacy JWT key %s: %w", path, err)
		}
	}
	return nil
}

// canonicalAuthPath resolves existing ancestors as well as missing descendants.
func canonicalAuthPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err == nil {
		return resolved, nil
	}
	if !os.IsNotExist(err) {
		return "", err
	}
	parent := filepath.Dir(absolute)
	if parent == absolute {
		return "", err
	}
	resolved, err = canonicalAuthPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolved, filepath.Base(absolute)), nil
}

func validateAuthDirectory(authDir, workspace string) error {
	auth, err := canonicalAuthPath(authDir)
	if err != nil {
		return fmt.Errorf("resolve auth directory: %w", err)
	}
	root, err := canonicalAuthPath(workspace)
	if err != nil {
		return fmt.Errorf("resolve workspace: %w", err)
	}
	// SameFile also handles case aliases on case-insensitive filesystems.
	rootInfo, rootErr := os.Stat(root)
	for current := auth; ; current = filepath.Dir(current) {
		if info, statErr := os.Stat(current); statErr == nil && rootErr == nil && os.SameFile(info, rootInfo) {
			return fmt.Errorf("auth directory must be outside browsable workspace")
		}
		if filepath.Dir(current) == current {
			break
		}
	}
	rel, err := filepath.Rel(root, auth)
	if err != nil {
		return nil
	} // Different volumes cannot be descendants.
	if rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))) {
		return fmt.Errorf("auth directory must be outside browsable workspace")
	}
	return nil
}
