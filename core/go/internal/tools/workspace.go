package tools

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"strings"
	"unicode/utf8"

	"aura.local/core/internal/ids"
)

var ErrBadPath = errors.New("workspace_bad_path")
var ErrSymlink = errors.New("workspace_symlink")
var ErrFileTooLarge = errors.New("workspace_too_large")

const (
	MaxFileBytes = 1 << 20
	maxPathLen   = 256
	maxDepth     = 8
)

// Workspace gives each task its own directory <root>/<task_id>/. All access is
// through os.Root (traversal-resistant) and refuses any symlink component.
type Workspace struct{ root string }

func NewWorkspace(root string) (*Workspace, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &Workspace{root: root}, nil
}

// Root is the workspace root directory.
func (w *Workspace) Root() string { return w.root }

// CleanPath validates a workspace-relative path.
func CleanPath(p string) (string, error) {
	if p == "" || len(p) > maxPathLen || !utf8.ValidString(p) || strings.ContainsAny(p, "\x00\\") {
		return "", fmt.Errorf("%w: invalid characters or length", ErrBadPath)
	}
	if strings.HasPrefix(p, "/") {
		return "", fmt.Errorf("%w: absolute path", ErrBadPath)
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == ".." {
			return "", fmt.Errorf("%w: traversal", ErrBadPath)
		}
	}
	c := path.Clean(p)
	if c == "." || strings.HasPrefix(c, "../") || strings.Count(c, "/") >= maxDepth {
		return "", fmt.Errorf("%w: %q", ErrBadPath, p)
	}
	return c, nil
}

func (w *Workspace) open(taskID string) (*os.Root, error) {
	if _, err := CleanPath(taskID); err != nil || strings.Contains(taskID, "/") {
		return nil, fmt.Errorf("%w: task id", ErrBadPath)
	}
	if err := os.Mkdir(path.Join(w.root, taskID), 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return nil, err
	}
	if fi, err := os.Lstat(path.Join(w.root, taskID)); err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: task directory", ErrSymlink)
	}
	return os.OpenRoot(path.Join(w.root, taskID))
}

// noSymlinks walks every existing component of rel and rejects symlinks.
func noSymlinks(r *os.Root, rel string) error {
	parts := strings.Split(rel, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		fi, err := r.Lstat(sub)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if fi.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("%w: %s", ErrSymlink, sub)
		}
	}
	return nil
}

// ReadResult is what workspace.read returns.
type ReadResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
	Content string `json:"content"`
}

// Read reads a UTF-8 text file from the task workspace.
func (w *Workspace) Read(taskID, rel string) (*ReadResult, error) {
	rel, err := CleanPath(rel)
	if err != nil {
		return nil, err
	}
	r, err := w.open(taskID)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := noSymlinks(r, rel); err != nil {
		return nil, err
	}
	fi, err := r.Lstat(rel)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: not a regular file", ErrBadPath)
	}
	if fi.Size() > MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	b, err := r.ReadFile(rel)
	if err != nil {
		return nil, err
	}
	return &ReadResult{Path: rel, Bytes: len(b), SHA256: Hash(b), Content: string(b)}, nil
}

// WriteResult is what workspace.write returns.
type WriteResult struct {
	Path    string `json:"path"`
	Bytes   int    `json:"bytes"`
	SHA256  string `json:"sha256"`
	Skipped bool   `json:"skipped"` // the file already had exactly this content
}

// Hash is the hex sha256 used for write idempotency.
func Hash(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

// Write atomically writes content: temp file in the same directory, fsync,
// rename, fsync directory. If the target already has the same sha256 the write
// is skipped, which makes retries with the same intent idempotent.
func (w *Workspace) Write(taskID, rel, content string) (*WriteResult, error) {
	rel, err := CleanPath(rel)
	if err != nil {
		return nil, err
	}
	if len(content) > MaxFileBytes {
		return nil, ErrFileTooLarge
	}
	r, err := w.open(taskID)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	if err := noSymlinks(r, rel); err != nil {
		return nil, err
	}
	data := []byte(content)
	res := &WriteResult{Path: rel, Bytes: len(data), SHA256: Hash(data)}
	if fi, err := r.Lstat(rel); err == nil {
		if !fi.Mode().IsRegular() {
			return nil, fmt.Errorf("%w: target is not a regular file", ErrBadPath)
		}
		if old, err := r.ReadFile(rel); err == nil && Hash(old) == res.SHA256 {
			res.Skipped = true
			return res, nil
		}
	}
	dir := path.Dir(rel)
	if dir != "." {
		if err := r.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		if err := noSymlinks(r, rel); err != nil {
			return nil, err
		}
	}
	tmp := path.Join(dir, ".tmp-"+ids.New())
	f, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		r.Remove(tmp)
		return nil, err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		r.Remove(tmp)
		return nil, err
	}
	f.Close()
	if err := r.Rename(tmp, rel); err != nil {
		r.Remove(tmp)
		return nil, err
	}
	if d, err := r.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return res, nil
}

// FileHash returns the sha256 of an existing workspace file ("" if absent).
func (w *Workspace) FileHash(taskID, rel string) (string, error) {
	res, err := w.Read(taskID, rel)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return res.SHA256, nil
}
