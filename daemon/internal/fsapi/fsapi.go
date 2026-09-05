// Package fsapi exposes a restricted view of the filesystem for folder browsing.
package fsapi

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/markusbug/Orchestrator/daemon/internal/protocol"
)

// ErrOutsideRoots is returned for paths not under any allowed root.
var ErrOutsideRoots = errors.New("fsapi: path outside allowed roots")

// Service browses directories under a fixed set of roots.
type Service struct {
	roots []string // resolved absolute paths
}

// New resolves the roots (symlinks evaluated) and returns a Service.
func New(roots []string) (*Service, error) {
	s := &Service{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		s.roots = append(s.roots, abs)
	}
	if len(s.roots) == 0 {
		return nil, errors.New("fsapi: no roots")
	}
	return s, nil
}

// Roots returns the resolved roots.
func (s *Service) Roots() []string { return append([]string(nil), s.roots...) }

// Resolve makes p absolute, resolves symlinks, and checks it lies under a root.
func (s *Service) Resolve(p string) (string, error) {
	if p == "" {
		return s.roots[0], nil
	}
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	for _, r := range s.roots {
		if real == r || strings.HasPrefix(real, r+string(filepath.Separator)) {
			return real, nil
		}
	}
	return "", ErrOutsideRoots
}

var skipDirs = map[string]bool{
	"node_modules": true, ".git": true, "target": true, "build": true, "dist": true,
	".cache": true, ".venv": true, "venv": true, "__pycache__": true, ".gradle": true,
	"Pods": true, ".dart_tool": true, ".idea": true, ".next": true, "vendor": true,
}

// List returns entries in dir, directories first, sorted by name.
func (s *Service) List(dir string, hidden bool) (protocol.FSListReply, error) {
	real, err := s.Resolve(dir)
	if err != nil {
		return protocol.FSListReply{}, err
	}
	entries, err := os.ReadDir(real)
	if err != nil {
		return protocol.FSListReply{}, err
	}
	reply := protocol.FSListReply{Path: real, Entries: []protocol.FSEntry{}}
	if parent := filepath.Dir(real); parent != real {
		if _, err := s.Resolve(parent); err == nil {
			reply.Parent = parent
		}
	}
	for _, e := range entries {
		name := e.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(real, name)
		info, err := e.Info()
		if err != nil {
			continue
		}
		isDir := e.IsDir()
		if e.Type()&fs.ModeSymlink != 0 {
			if st, err := os.Stat(full); err == nil {
				isDir = st.IsDir()
			}
		}
		fe := protocol.FSEntry{Name: name, Path: full, Dir: isDir, Mtime: info.ModTime().UnixMilli()}
		if isDir {
			if _, err := os.Stat(filepath.Join(full, ".git")); err == nil {
				fe.Git = true
			}
		} else {
			fe.Size = info.Size()
		}
		reply.Entries = append(reply.Entries, fe)
	}
	sort.SliceStable(reply.Entries, func(i, j int) bool {
		a, b := reply.Entries[i], reply.Entries[j]
		if a.Dir != b.Dir {
			return a.Dir
		}
		return strings.ToLower(a.Name) < strings.ToLower(b.Name)
	})
	return reply, nil
}

// SearchOptions bound a Search.
type SearchOptions struct {
	MaxDepth int
	Limit    int
	Timeout  time.Duration
}

// Search finds directories whose name contains query (case-insensitive)
// under root, breadth-limited by depth, count, and time.
func (s *Service) Search(ctx context.Context, root, query string, opt SearchOptions) ([]protocol.FSEntry, error) {
	real, err := s.Resolve(root)
	if err != nil {
		return nil, err
	}
	if opt.MaxDepth <= 0 {
		opt.MaxDepth = 6
	}
	if opt.Limit <= 0 {
		opt.Limit = 50
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 2 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, opt.Timeout)
	defer cancel()
	q := strings.ToLower(strings.TrimSpace(query))
	if q == "" {
		return []protocol.FSEntry{}, nil
	}
	out := []protocol.FSEntry{}
	errStop := errors.New("stop")
	walkErr := filepath.WalkDir(real, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if ctx.Err() != nil {
			return errStop
		}
		if !d.IsDir() {
			return nil
		}
		if p == real {
			return nil
		}
		name := d.Name()
		rel, _ := filepath.Rel(real, p)
		depth := strings.Count(rel, string(filepath.Separator)) + 1
		if strings.HasPrefix(name, ".") || skipDirs[name] {
			return fs.SkipDir
		}
		if strings.Contains(strings.ToLower(name), q) {
			info, ierr := d.Info()
			fe := protocol.FSEntry{Name: name, Path: p, Dir: true}
			if ierr == nil {
				fe.Mtime = info.ModTime().UnixMilli()
			}
			if _, err := os.Stat(filepath.Join(p, ".git")); err == nil {
				fe.Git = true
			}
			out = append(out, fe)
			if len(out) >= opt.Limit {
				return errStop
			}
		}
		if depth >= opt.MaxDepth {
			return fs.SkipDir
		}
		return nil
	})
	if walkErr != nil && walkErr != errStop {
		return out, walkErr
	}
	return out, nil
}
