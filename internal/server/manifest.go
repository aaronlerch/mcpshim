package server

import (
	"bytes"
	"fmt"
	"log"
	"os"
	"path/filepath"
)

// writeManifest renders the registry's current manifest to disk. Failures are
// logged but never propagated — a stale manifest is annoying but not fatal,
// and we'd rather complete the user's command than abort over a write error.
func (s *Server) writeManifest() {
	if s == nil || s.cfg == nil || s.registry == nil {
		return
	}
	path := s.cfg.Server.ManifestPath
	if path == "" {
		return
	}
	var buf bytes.Buffer
	if err := s.registry.RenderManifest(&buf); err != nil {
		log.Printf("[manifest] render failed: %v", err)
		return
	}
	if err := atomicWrite(path, buf.Bytes(), 0o644); err != nil {
		log.Printf("[manifest] write %s failed: %v", path, err)
		return
	}
}

// manifestContent renders the manifest to a string for direct CLI display.
func (s *Server) manifestContent() (string, error) {
	if s.registry == nil {
		return "", fmt.Errorf("registry not initialized")
	}
	var buf bytes.Buffer
	if err := s.registry.RenderManifest(&buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}

// atomicWrite writes data to a temp file and renames it into place, so an
// in-progress write never leaves a partial manifest visible.
func atomicWrite(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create dir: %w", err)
		}
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, mode); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}
