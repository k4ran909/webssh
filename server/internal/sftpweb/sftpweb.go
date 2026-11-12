// Package sftpweb is the HTTP file API over one SFTP connection. The gateway
// serves it for its own SSH sessions; the in-browser client for end-to-end
// sessions runs the very same handlers inside WebAssembly.
package sftpweb

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/sftp"
)

const (
	maxListEntries = 10_000
	maxRemoveAll   = 10_000
	MaxUpload      = 8 << 30
)

// Audit records a file action (metadata only, never contents).
type Audit func(action string, detail map[string]any)

func Err(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"code": code, "msg": msg})
}

func fsErr(w http.ResponseWriter, err error) {
	var se *sftp.StatusError
	switch {
	case errors.Is(err, fs.ErrNotExist):
		Err(w, http.StatusNotFound, "NOT_FOUND", "No such file or folder.")
	case errors.Is(err, fs.ErrPermission):
		Err(w, http.StatusForbidden, "PERMISSION", "Permission denied.")
	case errors.Is(err, fs.ErrExist):
		Err(w, http.StatusConflict, "EXISTS", "Something with that name already exists.")
	case errors.As(err, &se):
		Err(w, http.StatusBadRequest, "SFTP_ERROR", se.Error())
	default:
		Err(w, http.StatusBadGateway, "SFTP_ERROR", err.Error())
	}
}

// remotePath validates a POSIX path; relative paths resolve against the login directory.
func remotePath(p string) (string, error) {
	if p == "" || len(p) > 4096 || strings.ContainsRune(p, 0) {
		return "", errors.New("invalid path")
	}
	if strings.HasPrefix(p, "/") {
		return path.Clean(p), nil
	}
	return path.Clean("./" + p), nil
}

type Entry struct {
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Mode   string `json:"mode"`
	Perm   string `json:"perm"`
	IsDir  bool   `json:"isDir"`
	IsLink bool   `json:"isLink,omitempty"`
	MTime  int64  `json:"mtime"`
}

// List answers GET …/ls?path= with the folder's entries (folders first).
func List(w http.ResponseWriter, r *http.Request, c *sftp.Client) {
	p := r.URL.Query().Get("path")
	if p == "" {
		wd, err := c.Getwd()
		if err != nil {
			fsErr(w, err)
			return
		}
		p = wd
	}
	p, err := remotePath(p)
	if err != nil {
		Err(w, http.StatusBadRequest, "BAD_PATH", err.Error())
		return
	}
	if real, err := c.RealPath(p); err == nil {
		p = real
	}
	infos, err := c.ReadDir(p)
	if err != nil {
		fsErr(w, err)
		return
	}
	if len(infos) > maxListEntries {
		infos = infos[:maxListEntries]
	}
	out := make([]Entry, 0, len(infos))
	for _, fi := range infos {
		e := Entry{Name: fi.Name(), Size: fi.Size(), Mode: fi.Mode().String(), Perm: fmt.Sprintf("%04o", fi.Mode().Perm()),
			IsDir: fi.IsDir(), MTime: fi.ModTime().Unix()}
		if fi.Mode()&os.ModeSymlink != 0 {
			e.IsLink = true
			if t, err := c.Stat(path.Join(p, fi.Name())); err == nil {
				e.IsDir = t.IsDir()
			}
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].IsDir != out[j].IsDir {
			return out[i].IsDir
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(map[string]any{"path": p, "entries": out, "truncated": len(infos) == maxListEntries})
}

// Read answers GET …/file?path= by streaming the file. It is always an attachment of type
// application/octet-stream so remote content can never render as a page on
// this origin.
func Read(w http.ResponseWriter, r *http.Request, c *sftp.Client, host string, audit Audit) {
	p, err := remotePath(r.URL.Query().Get("path"))
	if err != nil {
		Err(w, http.StatusBadRequest, "BAD_PATH", err.Error())
		return
	}
	f, err := c.Open(p)
	if err != nil {
		fsErr(w, err)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		fsErr(w, err)
		return
	}
	if st.IsDir() {
		Err(w, http.StatusBadRequest, "IS_DIR", "That's a folder.")
		return
	}
	hd := w.Header()
	hd.Set("Content-Type", "application/octet-stream")
	hd.Set("Content-Disposition", "attachment; filename*=UTF-8''"+url.PathEscape(path.Base(p)))
	hd.Set("Content-Length", strconv.FormatInt(st.Size(), 10))
	hd.Set("Cache-Control", "no-store")
	n, _ := f.WriteTo(w)
	if r.URL.Query().Get("download") == "1" {
		audit("sftp.download", map[string]any{"host": host, "path": p, "bytes": n})
	}
}

// Write answers PUT …/file?path=[&overwrite=1] with the request body as the file.
func Write(w http.ResponseWriter, r *http.Request, c *sftp.Client, host string, audit Audit) {
	q := r.URL.Query()
	p, err := remotePath(q.Get("path"))
	if err != nil {
		Err(w, http.StatusBadRequest, "BAD_PATH", err.Error())
		return
	}
	flags := os.O_WRONLY | os.O_CREATE | os.O_TRUNC
	if q.Get("overwrite") != "1" {
		// SFTP v3 has no "already exists" status (servers answer a generic
		// failure to O_EXCL), so check first; O_EXCL still closes the race.
		if _, err := c.Lstat(p); err == nil {
			fsErr(w, fs.ErrExist)
			return
		}
		flags |= os.O_EXCL
	}
	f, err := c.OpenFile(p, flags)
	if err != nil {
		fsErr(w, err)
		return
	}
	n, err := f.ReadFrom(http.MaxBytesReader(w, r.Body, MaxUpload))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	audit("sftp.upload", map[string]any{"host": host, "path": p, "bytes": n})
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]int64{"bytes": n})
}

// Op answers POST …/op with {op: mkdir|rename|remove|chmod, path, to, mode}.
func Op(w http.ResponseWriter, r *http.Request, c *sftp.Client, host string, audit Audit) {
	var req struct {
		Op   string `json:"op"`
		Path string `json:"path"`
		To   string `json:"to,omitempty"`
		Mode string `json:"mode,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&req); err != nil {
		Err(w, http.StatusBadRequest, "BAD_REQUEST", "Malformed request.")
		return
	}
	p, err := remotePath(req.Path)
	if err != nil {
		Err(w, http.StatusBadRequest, "BAD_PATH", err.Error())
		return
	}
	detail := map[string]any{"host": host, "path": p}
	switch req.Op {
	case "mkdir":
		err = c.Mkdir(p)
	case "rename":
		var to string
		if to, err = remotePath(req.To); err == nil {
			detail["to"] = to
			if err = c.PosixRename(p, to); err != nil {
				err = c.Rename(p, to) // servers without the posix-rename extension
			}
		}
	case "remove":
		var st os.FileInfo
		if st, err = c.Lstat(p); err == nil {
			if st.IsDir() {
				err = removeAll(c, p)
			} else {
				err = c.Remove(p)
			}
		}
	case "chmod":
		m, perr := strconv.ParseUint(req.Mode, 8, 32)
		if perr != nil || m > 0o7777 {
			Err(w, http.StatusBadRequest, "BAD_MODE", "Mode must be octal, e.g. 0644.")
			return
		}
		detail["mode"] = req.Mode
		err = c.Chmod(p, os.FileMode(m))
	default:
		Err(w, http.StatusBadRequest, "BAD_OP", "Unknown operation.")
		return
	}
	if err != nil {
		fsErr(w, err)
		return
	}
	if req.Op != "mkdir" {
		audit("sftp."+req.Op, detail)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}

// removeAll deletes a tree depth-first without following symlinks, with a
// cap so a mistaken click on / can't run forever.
func removeAll(c *sftp.Client, root string) error {
	count := 0
	var walk func(p string) error
	walk = func(p string) error {
		infos, err := c.ReadDir(p)
		if err != nil {
			return err
		}
		for _, fi := range infos {
			if count++; count > maxRemoveAll {
				return fmt.Errorf("refusing to delete more than %d entries at once", maxRemoveAll)
			}
			child := path.Join(p, fi.Name())
			if fi.IsDir() && fi.Mode()&os.ModeSymlink == 0 {
				if err := walk(child); err != nil {
					return err
				}
			} else if err := c.Remove(child); err != nil {
				return err
			}
		}
		return c.RemoveDirectory(p)
	}
	return walk(root)
}
