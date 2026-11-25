package plugininstall

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

const (
	maxArchive = 16 << 20
	maxBundle  = 32 << 20
	maxFile    = 8 << 20
	maxEntries = 2048
)

var deviceName = regexp.MustCompile(`(?i)^(CON|PRN|AUX|NUL|CONIN\$|CONOUT\$|(?:COM|LPT)[1-9¹²³])$`)

type bundle struct {
	ctx     context.Context
	dir     string
	total   int64
	seen    map[string]string // canonical spellings, including implicit parent dirs
	entries int
}

func newBundle(ctx context.Context, dir string) *bundle {
	return &bundle{ctx: ctx, dir: dir, seen: map[string]string{}}
}

// Every input format uses the same portable filename and resource policy. The
// destination is a fresh private directory: links and executable bits are never
// copied, and existing files (including case-insensitive aliases) never overwritten.
func (b *bundle) reserve(name string, size int64, directory bool) (bool, error) {
	if err := b.ctx.Err(); err != nil {
		return false, err
	}
	b.entries++
	if b.entries > maxEntries {
		return false, errors.New("too many plugin entries")
	}
	if !fs.ValidPath(name) || name == "." || len(name) > 1024 || strings.Count(name, "/") >= 32 || strings.ContainsAny(name, `\:<>"|?*`) || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return false, fmt.Errorf("invalid plugin path %q", name)
	}
	parts := strings.Split(name, "/")
	skip := false
	for _, part := range parts {
		stem, _, _ := strings.Cut(part, ".")
		if len(part) > 255 || strings.HasSuffix(part, ".") || strings.HasSuffix(part, " ") || deviceName.MatchString(strings.TrimRight(stem, " ")) {
			return false, fmt.Errorf("invalid plugin path %q", name)
		}
		if strings.EqualFold(part, ".git") {
			skip = true
		}
	}
	if skip {
		return false, nil
	}
	for i := range parts {
		p := strings.Join(parts[:i+1], "/")
		key := strings.ToLower(p)
		if i+1 < len(parts) || directory {
			p += "/"
		}
		if old, ok := b.seen[key]; ok && (old != p || !strings.HasSuffix(p, "/")) {
			return false, fmt.Errorf("conflicting plugin path %q", name)
		}
		b.seen[key] = p
	}
	if len(b.seen) > maxEntries {
		return false, errors.New("too many plugin paths")
	}
	if !directory {
		if size < 0 || size > maxFile || size > maxBundle-b.total {
			return false, errors.New("plugin exceeds size limit")
		}
		b.total += size
	}
	return true, nil
}

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

func (b *bundle) write(name string, r io.Reader, size int64) error {
	path := filepath.Join(b.dir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(contextReader{b.ctx, r}, size+1))
	if err != nil {
		return err
	}
	if n != size {
		return fmt.Errorf("unexpected size for %q", name)
	}
	return f.Close()
}

func (b *bundle) unzip(r io.ReaderAt, size int64) error {
	if size > maxArchive {
		return errors.New("plugin ZIP exceeds size limit")
	}
	z, err := zip.NewReader(r, size)
	if err != nil {
		return err
	}
	if len(z.File) > maxEntries {
		return errors.New("too many plugin ZIP entries")
	}
	for _, f := range z.File {
		mode := f.Mode()
		name := strings.TrimSuffix(f.Name, "/")
		directory := mode.IsDir()
		n := int64(f.UncompressedSize64)
		keep, err := b.reserve(name, n, directory)
		if err != nil {
			return err
		}
		if !keep {
			continue
		}
		switch {
		case mode&fs.ModeType == fs.ModeDir:
			if n != 0 {
				return errors.New("ZIP directory contains data")
			}
			err = os.MkdirAll(filepath.Join(b.dir, filepath.FromSlash(name)), 0700)
		case mode.IsRegular():
			var in io.ReadCloser
			in, err = f.Open()
			if err == nil {
				err = b.write(name, in, n)
				in.Close()
			}
		default:
			return fmt.Errorf("unsupported ZIP entry %q", name)
		}
		if err != nil {
			return err
		}
	}
	return nil
}

func copyLocal(ctx context.Context, source, dir string) error {
	st, err := os.Lstat(source)
	if err != nil {
		return err
	}
	b := newBundle(ctx, dir)
	if st.IsDir() {
		root, err := os.OpenRoot(source)
		if err != nil {
			return err
		}
		defer root.Close()
		actual, err := root.Stat(".")
		if err != nil {
			return err
		}
		if !os.SameFile(st, actual) {
			return errors.New("plugin directory changed while opening")
		}
		return b.copyDir(root, "")
	}
	if !st.Mode().IsRegular() {
		return errors.New("source is not a regular ZIP file")
	}
	f, err := os.OpenFile(source, readFlags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, actual) {
		return errors.New("plugin ZIP changed while opening")
	}
	return b.unzip(f, actual.Size())
}

// Open relative to the already-verified parent handle. A concurrent change of
// any ancestor's name cannot redirect the rest of the traversal.
func (b *bundle) copyChild(root *os.Root, prefix, base string) error {
	name := base
	if prefix != "" {
		name = prefix + "/" + base
	}
	st, err := root.Lstat(base)
	if err != nil {
		return err
	}
	keep, err := b.reserve(name, st.Size(), st.IsDir())
	if err != nil || !keep {
		return err
	}
	if st.IsDir() {
		sub, err := root.OpenRoot(base)
		if err != nil {
			return err
		}
		defer sub.Close()
		opened, err := sub.Stat(".")
		if err != nil {
			return err
		}
		if !os.SameFile(st, opened) {
			return errors.New("plugin directory changed while opening")
		}
		if err = os.MkdirAll(filepath.Join(b.dir, filepath.FromSlash(name)), 0700); err != nil {
			return err
		}
		return b.copyDir(sub, name)
	}
	if !st.Mode().IsRegular() {
		return fmt.Errorf("unsupported plugin file %q", name)
	}
	f, err := root.OpenFile(base, readFlags, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(st, opened) {
		return errors.New("plugin file changed while opening")
	}
	return b.write(name, f, st.Size())
}

func (b *bundle) copyDir(root *os.Root, prefix string) error {
	dir, err := root.Open(".")
	if err != nil {
		return err
	}
	defer dir.Close()
	for {
		names, err := dir.Readdirnames(64)
		if err != nil && err != io.EOF {
			return err
		}
		for _, base := range names {
			if err := b.copyChild(root, prefix, base); err != nil {
				return err
			}
		}
		if err == io.EOF {
			return nil
		}
	}
}
