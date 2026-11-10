package main

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

// createTarGz writes srcDir to dest as a gzip-compressed tar whose single
// top-level directory is topName (equivalent to `tar cvaf dest topName/`).
// It returns the archive size.
func createTarGz(ctx context.Context, srcDir, topName, dest string) (int64, error) {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return 0, err
	}
	tmp := dest + ".partial"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return 0, err
	}
	defer func() {
		f.Close()
		os.Remove(tmp)
	}()

	bw := bufio.NewWriterSize(f, 1<<20)
	gz := gzip.NewWriter(bw)
	tw := tar.NewWriter(gz)

	err = filepath.WalkDir(srcDir, func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(srcDir, p)
		if err != nil {
			return err
		}
		name := topName
		if rel != "." {
			name = path.Join(topName, filepath.ToSlash(rel))
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			if link, err = os.Readlink(p); err != nil {
				return err
			}
		} else if !info.Mode().IsRegular() && !info.IsDir() {
			return nil // skip sockets, devices, etc.
		}
		hdr, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		hdr.Name = name
		if info.IsDir() {
			hdr.Name += "/"
		}
		// Do not leak the runner's random UID; make files readable on extract.
		hdr.Uid, hdr.Gid, hdr.Uname, hdr.Gname = 0, 0, "", ""
		if info.IsDir() {
			hdr.Mode = 0o755
		} else if info.Mode().IsRegular() {
			hdr.Mode = 0o644
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		src, err := os.Open(p)
		if err != nil {
			return err
		}
		defer src.Close()
		if _, err := io.Copy(tw, src); err != nil {
			return fmt.Errorf("%s: %w", rel, err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	if err := tw.Close(); err != nil {
		return 0, err
	}
	if err := gz.Close(); err != nil {
		return 0, err
	}
	if err := bw.Flush(); err != nil {
		return 0, err
	}
	if err := f.Close(); err != nil {
		return 0, err
	}
	if err := os.Rename(tmp, dest); err != nil {
		return 0, err
	}
	st, err := os.Stat(dest)
	if err != nil {
		return 0, err
	}
	return st.Size(), nil
}
