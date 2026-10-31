// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"sigs.k8s.io/kustomize/api/konfig"
	"sigs.k8s.io/kustomize/api/types"
	"sigs.k8s.io/yaml"
)

// acquiredBase describes an immutable whole-repository snapshot, never just the
// selected subtree. Identity must bind canonical repository, authorization context
// and full resolved SHA. The engine treats it as opaque and never logs it.
type acquiredBase struct {
	RepositoryDir, SubPath, Identity string
	Cleanup                          func()
}

// acquireBase resolves refs afresh and acquires immutable content. The production
// closure owns scratch placement and credential scoping. Cleanup is called even
// when acquisition returns both an error and a partial result.
type acquireBase func(context.Context, gitBaseReference) (acquiredBase, error)
type preparedBaseIdentity struct {
	Reference gitBaseReference
	Identity  string
}
type preparedKustomize struct {
	Root       string
	Identities []preparedBaseIdentity
	Cleanup    func()
}

// prepareKustomize stages and preflights before krusty OR a render-cache lookup.
// Root cleanup remains caller-owned; acquired base cleanups run after staging.
// Successful callers must defer result.Cleanup (also on render failure/cancel).
func prepareKustomize(ctx context.Context, root acquiredBase, scratch string, acquire acquireBase) (_ *preparedKustomize, err error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir, err := os.MkdirTemp(scratch, "ddo-kustomize-stage-")
	if err != nil {
		return nil, err
	}
	canonical, canonicalErr := filepath.Abs(dir)
	if canonicalErr == nil {
		canonical, canonicalErr = filepath.EvalSymlinks(canonical)
	}
	if canonicalErr != nil {
		_ = os.RemoveAll(dir)
		return nil, canonicalErr
	}
	dir = canonical
	var once sync.Once
	cleanup := func() { once.Do(func() { _ = os.RemoveAll(dir) }) }
	defer func() {
		if err != nil {
			cleanup()
		}
	}()
	e := preparationEngine{stage: dir, rootIdentity: root.Identity, acquire: acquire, repositories: map[string]string{}, active: map[string]bool{}, done: map[string]bool{}, originals: map[string][]byte{}}
	repo, err := e.stageRepository(ctx, root)
	if err != nil {
		return nil, err
	}
	sub := root.SubPath
	if sub == "" {
		sub = "."
	}
	selected, err := preparationLocalPath(repo, repo, sub)
	if err != nil {
		return nil, err
	}
	if err := e.visit(ctx, repo, selected); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return &preparedKustomize{Root: selected, Identities: e.identities, Cleanup: cleanup}, nil
}

type preparationEngine struct {
	stage        string
	rootIdentity string
	acquire      acquireBase
	repositories map[string]string
	active, done map[string]bool
	identities   []preparedBaseIdentity
	originals    map[string][]byte // immutable input keyed by resolved staged filename
}

func (e *preparationEngine) stageRepository(ctx context.Context, base acquiredBase) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if base.Identity == "" {
		return "", errors.New("repository snapshot requires an associated immutable identity")
	}
	if base.RepositoryDir == "" {
		return "", errors.New("repository snapshot requires an explicit repository boundary")
	}
	if dir, ok := e.repositories[base.Identity]; ok {
		return dir, nil
	}
	src, err := filepath.Abs(base.RepositoryDir)
	if err != nil {
		return "", err
	}
	src, err = filepath.EvalSymlinks(src)
	if err != nil {
		return "", err
	}
	stage, err := filepath.EvalSymlinks(e.stage)
	if err != nil {
		return "", err
	}
	if preparationWithin(src, stage) {
		return "", errors.New("scratch directory must not be inside an input repository")
	}
	sum := sha256.Sum256([]byte(base.Identity))
	dest := filepath.Join(e.stage, "repos", hex.EncodeToString(sum[:]))
	if base.Identity == e.rootIdentity {
		// ValueAddTransformer's default value is the loader root's basename.
		// Keep the original root name so staging local-only input is transparent.
		dest = filepath.Join(e.stage, "root", filepath.Base(src))
	}
	if err := copyPreparationRepository(ctx, src, dest); err != nil {
		return "", err
	}
	e.repositories[base.Identity] = dest
	return dest, nil
}

func copyPreparationRepository(ctx context.Context, src, dest string) error {
	return filepath.WalkDir(src, func(p string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		rel, err := filepath.Rel(src, p)
		if err != nil {
			return err
		}
		if entry.Name() == ".git" {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		target := filepath.Join(dest, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0700)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			resolved, err := filepath.EvalSymlinks(p)
			if err != nil {
				return err
			}
			if !preparationWithin(src, resolved) {
				return errors.New("symlink escapes repository boundary")
			}
			link, err := filepath.Rel(filepath.Dir(p), resolved)
			if err != nil {
				return err
			}
			return os.Symlink(link, target) // #nosec G122 -- Private serial stage; resolved source containment checked above, no concurrent repository writers.
		}
		if !entry.Type().IsRegular() {
			return errors.New("unsupported special file in repository")
		}
		data, err := os.ReadFile(p) // #nosec G122 -- Completed private checkout is immutable during this serial copy; symlinks handled above.
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0600)
	})
}

func preparationWithin(repo, target string) bool {
	rel, err := filepath.Rel(repo, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}

// preparationLocalPath checks both lexical and resolved boundaries. Checking real
// paths also collapses symlink aliases for cycle detection.
func preparationLocalPath(repo, dir, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) {
		return "", errors.New("loading paths must be nonempty and repository-relative")
	}
	if _, remote, err := classifyGitBase(name); remote || err != nil {
		return "", errors.New("unsupported remote loading path")
	}
	p := filepath.Join(dir, name)
	if !preparationWithin(repo, p) {
		return "", errors.New("loading path escapes repository boundary")
	}
	p, err := filepath.EvalSymlinks(p)
	if err != nil {
		return "", err
	}
	boundary, err := filepath.EvalSymlinks(repo)
	if err != nil {
		return "", err
	}
	if !preparationWithin(boundary, p) {
		return "", errors.New("loading symlink escapes repository boundary")
	}
	return p, nil
}

func (e *preparationEngine) visit(ctx context.Context, repo, dir string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e.active[dir] {
		return errors.New("cycle in kustomization references")
	}
	if e.done[dir] {
		return nil
	}
	e.active[dir] = true
	defer delete(e.active, dir)
	filename, err := preparationKustomizationFile(dir)
	if err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(filename)
	if err != nil {
		return err
	}
	// A reachable regular target may already have been localized before an alias
	// is visited. Always decode its original bytes, relative to this directory.
	data, ok := e.originals[resolved]
	if !ok {
		data, err = os.ReadFile(filename)
		if err != nil {
			return err
		}
		e.originals[resolved] = data
	}
	var k types.Kustomization
	if err := preparationDecode(data, &k); err != nil {
		return fmt.Errorf("invalid kustomization: %w", err)
	}
	if fields := k.EnforceFields(); len(fields) != 0 {
		return fmt.Errorf("invalid kustomization: %s", strings.Join(fields, "; "))
	}
	if err := preflightKustomization(repo, dir, &k); err != nil {
		return err
	}
	changed := false
	for _, refs := range [][]string{k.Resources, k.Components, k.Bases} { //nolint:staticcheck // Legacy bases remain supported and must be preflighted/localized.
		for i, raw := range refs {
			ref, remote, err := classifyGitBase(raw)
			if err != nil {
				return err
			}
			if remote {
				baseRepo, selected, err := e.acquireReference(ctx, ref)
				if err != nil {
					return fmt.Errorf("prepare base %s (ref %q): %w", ref.Repository, ref.Ref, err)
				}
				if err := e.visit(ctx, baseRepo, selected); err != nil {
					return err
				}
				replacement, err := filepath.Rel(dir, selected)
				if err != nil {
					return err
				}
				refs[i], changed = filepath.ToSlash(replacement), true
				continue
			}
			local, err := preparationLocalPath(repo, dir, raw)
			if err != nil {
				return err
			}
			info, err := os.Stat(local)
			if err != nil {
				return err
			}
			if info.IsDir() {
				if err := e.visit(ctx, repo, local); err != nil {
					return err
				}
			}
		}
	}
	if changed {
		// All fields have been strictly decoded; rewriting cannot silently discard
		// unknown loading fields. Local-only files are kept byte-for-byte intact.
		data, err := yaml.Marshal(k)
		if err != nil {
			return err
		}
		// Materialize this directory's rewritten file rather than following a
		// symlink and changing the input seen by other directory aliases.
		info, err := os.Lstat(filename)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if err := os.Remove(filename); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filename, data, 0600); err != nil {
			return err
		}
	}
	e.done[dir] = true
	return nil
}

func (e *preparationEngine) acquireReference(ctx context.Context, ref gitBaseReference) (repo, selected string, err error) {
	if e.acquire == nil {
		return "", "", errors.New("remote base acquisition is not configured")
	}
	base, err := e.acquire(ctx, ref)
	if base.Cleanup != nil {
		defer base.Cleanup()
	}
	if err != nil {
		return "", "", err
	}
	if base.SubPath == "" {
		base.SubPath = "."
	}
	if filepath.ToSlash(filepath.Clean(base.SubPath)) != ref.SubPath {
		return "", "", errors.New("acquisition selected a different repository subdirectory")
	}
	repo, err = e.stageRepository(ctx, base)
	if err != nil {
		return "", "", err
	}
	selected, err = preparationLocalPath(repo, repo, base.SubPath)
	if err != nil {
		return "", "", err
	}
	e.identities = append(e.identities, preparedBaseIdentity{Reference: ref, Identity: base.Identity})
	return repo, selected, nil
}

func preparationKustomizationFile(dir string) (string, error) {
	var filename string
	for _, name := range konfig.RecognizedKustomizationFileNames() {
		p := filepath.Join(dir, name)
		info, err := os.Stat(p)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if info.IsDir() {
			return "", errors.New("kustomization filename is a directory")
		}
		if filename != "" {
			return "", errors.New("multiple recognized kustomization files")
		}
		filename = p
	}
	if filename == "" {
		return "", errors.New("directory has no recognized kustomization file")
	}
	return filename, nil
}
