// Package archive packs a folder as the .tar.gz `mw deploy` sends: what the
// build needs, without what the folder's ignore rules leave out.
//
// The rules are `.mwignore` when the folder has one, otherwise its
// `.gitignore` files (each speaking for its own directory), in git's syntax.
// `.git`, `.mw` and `.DS_Store` are never sent.
package archive

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Result is what was packed.
type Result struct {
	// Path of the archive, in the system's temporary directory: remove it.
	Path  string
	Size  int64
	Files int
	// Rules names the ignore file(s) honoured: ".mwignore", ".gitignore" or "".
	Rules string
}

// Pack writes dir as a .tar.gz. maxBytes > 0 stops early once the archive
// passes it (the error says by how much the folder is too big).
func Pack(dir string, maxBytes int64) (*Result, error) {
	root, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if st, err := os.Stat(root); err != nil {
		return nil, err
	} else if !st.IsDir() {
		return nil, fmt.Errorf("%s is not a folder", dir)
	}

	res := &Result{}
	var rules []rule
	mwignore := filepath.Join(root, ".mwignore")
	useGit := true
	if _, err := os.Stat(mwignore); err == nil {
		if rules, err = parseIgnore(mwignore, ""); err != nil {
			return nil, err
		}
		useGit = false
		res.Rules = ".mwignore"
	}

	out, err := os.CreateTemp("", "mw-deploy-*.tar.gz")
	if err != nil {
		return nil, err
	}
	res.Path = out.Name()
	fail := func(err error) (*Result, error) {
		out.Close()
		os.Remove(res.Path)
		return nil, err
	}
	counted := &countingWriter{w: out, max: maxBytes}
	gz, _ := gzip.NewWriterLevel(counted, gzip.BestCompression)
	tw := tar.NewWriter(gz)

	// What is written first, ahead of the walk: the folder's settings file, so
	// whoever reads the archive for it finds it in its first block.
	first := ""
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "." {
			if useGit {
				if err := addGitignore(&rules, p, "", res); err != nil {
					return err
				}
			}
			// Now that the root's rules are read: the settings file, unless they leave it out.
			if st, err := os.Lstat(filepath.Join(root, ConfigFile)); err == nil && st.Mode().IsRegular() && !ignored(rules, ConfigFile, false) {
				first = ConfigFile
				return addFile(tw, filepath.Join(root, ConfigFile), ConfigFile, st, res)
			}
			return nil
		}
		if rel == first {
			return nil
		}
		if alwaysLeftOut(rel) || ignored(rules, rel, d.IsDir()) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() && useGit {
			if err := addGitignore(&rules, p, rel, res); err != nil {
				return err
			}
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // sockets, devices, pipes: nothing a build reads
		}
		if info.Mode().IsRegular() {
			return addFile(tw, p, rel, info, res)
		}
		h, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		h.Name = rel
		if info.IsDir() {
			h.Name += "/"
		}
		plain(h, info)
		return tw.WriteHeader(h)
	})
	if err == nil {
		err = tw.Close()
	}
	if err == nil {
		err = gz.Close()
	}
	if err == nil {
		err = out.Close()
	}
	if err != nil {
		if errors.Is(err, errTooLarge) {
			return fail(fmt.Errorf("the folder is over %s compressed: leave out what the build does not need (dependencies, build output, media) in a .mwignore", humanBytes(maxBytes)))
		}
		return fail(err)
	}
	res.Size = counted.n
	if res.Files == 0 {
		os.Remove(res.Path)
		return nil, fmt.Errorf("nothing to deploy: %s has no files (or its ignore rules leave all of them out)", dir)
	}
	return res, nil
}

// ConfigFile is the folder's settings file (the port, the commands, the
// size): packed first.
const ConfigFile = "mw.toml"

// plain strips from a header what means nothing to the build: whose files
// they were on this machine. Whole seconds keep every header plain ustar (PAX
// only for long names).
func plain(h *tar.Header, info fs.FileInfo) {
	h.Uid, h.Gid, h.Uname, h.Gname = 0, 0, "", ""
	h.ModTime = info.ModTime().Truncate(time.Second)
}

// addFile writes one regular file, at p, into the archive as rel.
func addFile(tw *tar.Writer, p, rel string, info fs.FileInfo, res *Result) error {
	h, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	h.Name = rel
	plain(h, info)
	if err := tw.WriteHeader(h); err != nil {
		return err
	}
	f, err := os.Open(p)
	if err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	f.Close()
	if err != nil {
		return err
	}
	res.Files++
	return nil
}

func addGitignore(rules *[]rule, dir, rel string, res *Result) error {
	more, err := parseIgnore(filepath.Join(dir, ".gitignore"), rel)
	if err != nil {
		return err
	}
	if len(more) > 0 {
		*rules = append(*rules, more...)
		res.Rules = ".gitignore"
	}
	return nil
}

var errTooLarge = errors.New("archive too large")

type countingWriter struct {
	w   io.Writer
	n   int64
	max int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	if err == nil && c.max > 0 && c.n > c.max {
		return n, errTooLarge
	}
	return n, err
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// HumanBytes prints a size the way the CLI does elsewhere.
func HumanBytes(n int64) string { return strings.TrimSpace(humanBytes(n)) }
