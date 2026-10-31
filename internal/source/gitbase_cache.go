// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// Limit retained uncompressed content (including tar headers) to 8 MiB and 32
// entries, leaving headroom in a 128 MiB container. A cache's gate serializes
// content fetch/snapshot/restore: concurrent misses cannot multiply snapshot
// buffers, and a resident identity is fetched once. Ref/credential resolution
// happens outside this gate and never gets memoized. This is not a limit on
// go-git pack memory, checkout disk space, directory metadata, or caller staging.
const defaultGitBaseCacheBytes = 8 << 20
const maxGitBaseCacheEntries = 32

type gitBaseEntry struct {
	identity string
	data     []byte
}

type gitBaseCache struct {
	gate    chan struct{}
	limit   int
	used    int
	entries []gitBaseEntry // oldest first; accessed only while gate is held
}

func newGitBaseCache(limit int) *gitBaseCache {
	return &gitBaseCache{gate: make(chan struct{}, 1), limit: limit}
}

func (c *gitBaseCache) lock(ctx context.Context) error {
	select {
	case c.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *gitBaseCache) unlock() { <-c.gate }

func (c *gitBaseCache) get(identity string) ([]byte, bool) {
	if c == nil {
		return nil, false
	}
	for i, entry := range c.entries {
		if entry.identity == identity {
			copy(c.entries[i:], c.entries[i+1:])
			c.entries[len(c.entries)-1] = entry
			return entry.data, true
		}
	}
	return nil, false
}

// put takes ownership of data. Never expose retained bytes outside acquisition.
func (c *gitBaseCache) put(identity string, data []byte) {
	if len(data) == 0 || len(data) > c.limit {
		return
	}
	for len(c.entries) > 0 && (c.used+len(data) > c.limit || len(c.entries) >= maxGitBaseCacheEntries) {
		c.used -= len(c.entries[0].data)
		copy(c.entries, c.entries[1:])
		c.entries[len(c.entries)-1] = gitBaseEntry{}
		c.entries = c.entries[:len(c.entries)-1]
	}
	c.entries = append(c.entries, gitBaseEntry{identity: identity, data: data})
	c.used += len(data)
}

var errGitSnapshotTooLarge = errors.New("git snapshot exceeds cache capacity")

type gitSnapshotWriter struct {
	check     func() error
	file      *os.File
	remaining int64
}

func (w *gitSnapshotWriter) Write(p []byte) (int, error) {
	if err := w.check(); err != nil {
		return 0, err
	}
	if int64(len(p)) > w.remaining {
		return 0, errGitSnapshotTooLarge
	}
	n, err := w.file.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// Build the bounded archive on scratch disk first, then allocate exactly its
// size. Never os.ReadFile an arbitrary repository file. Oversized content stays
// usable in its checkout but is not retained. Peak snapshot bytes are at most
// twice the cache byte limit (resident bytes + one candidate), without geometric
// bytes.Buffer growth. io.Copy uses a fixed 32 KiB transfer buffer.
func snapshotGitCheckout(ctx context.Context, dir string, limit int) ([]byte, error) {
	if limit <= 0 {
		return nil, nil
	}
	f, err := os.CreateTemp(filepath.Dir(dir), "ddo-git-snapshot-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close(); _ = os.Remove(f.Name()) }()
	tw := tar.NewWriter(&gitSnapshotWriter{check: ctx.Err, file: f, remaining: int64(limit)})
	err = filepath.WalkDir(dir, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if path == dir {
			return nil
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		link := ""
		if info.Mode()&os.ModeSymlink != 0 {
			link, err = os.Readlink(path)
			if err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		header.Name = filepath.ToSlash(name)
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		input, err := os.Open(path) // #nosec G122 -- Completed private checkout, serialized snapshot ownership; nonregular entries (including symlinks) skipped above.
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(tw, input)
		closeErr := input.Close()
		return errors.Join(copyErr, closeErr)
	})
	if err == nil {
		err = tw.Close()
	}
	if errors.Is(err, errGitSnapshotTooLarge) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	size, err := f.Seek(0, io.SeekCurrent)
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	data := make([]byte, int(size))
	_, err = io.ReadFull(f, data)
	return data, err
}

// Only internally-produced archives reach this function. os.Root additionally
// confines writes even in the presence of symlinks. Symlinks are preserved, not
// followed while snapshotting; preparation still validates operand boundaries.
func restoreGitSnapshot(ctx context.Context, dir string, data []byte) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(bytes.NewReader(data))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			err = root.MkdirAll(header.Name, 0o700)
		case tar.TypeSymlink:
			err = root.Symlink(header.Linkname, header.Name)
		case tar.TypeReg:
			var f *os.File
			mode := os.FileMode(0o600)
			if header.Mode&0o111 != 0 {
				mode = 0o700
			}
			f, err = root.OpenFile(header.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
			if err == nil {
				_, copyErr := io.CopyN(f, tr, header.Size)
				err = errors.Join(copyErr, f.Close())
			}
		default:
			return errors.New("source: unsupported Git snapshot entry")
		}
		if err != nil {
			return err
		}
	}
}
