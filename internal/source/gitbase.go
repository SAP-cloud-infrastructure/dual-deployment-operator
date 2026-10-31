// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/go-git/go-git/v5/plumbing/transport"
	httpauth "github.com/go-git/go-git/v5/plumbing/transport/http"
)

type gitOrigin struct{ scheme, hostname, port string }

func gitOriginOf(raw string) (gitOrigin, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Scheme == "" {
		return gitOrigin{}, errors.New("source: invalid Git URL (use authSecretRef, not embedded credentials)")
	}
	o := gitOrigin{scheme: strings.ToLower(u.Scheme), hostname: strings.ToLower(u.Hostname()), port: u.Port()}
	if o.port != "" {
		port, err := strconv.ParseUint(o.port, 10, 16)
		if err != nil {
			return gitOrigin{}, errors.New("source: invalid Git origin port")
		}
		o.port = strconv.FormatUint(port, 10)
	}
	if o.port == "" {
		switch o.scheme {
		case "https":
			o.port = "443"
		case "http":
			o.port = "80"
		}
	}
	return o, nil
}

// canonicalGitRepository accepts already-normalized repositories, not authoring
// syntax. The preparation parser enforces HTTPS; file/http are local test seams.
func canonicalGitRepository(raw string) (string, error) {
	o, err := gitOriginOf(raw)
	if err != nil {
		return "", err
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", errors.New("source: invalid Git repository URL")
	}
	if u.RawQuery != "" || u.Fragment != "" || (o.scheme != "https" && o.scheme != "http" && o.scheme != "file") {
		return "", errors.New("source: expected normalized Git repository URL")
	}
	u.Scheme = o.scheme
	if o.scheme != "file" {
		if o.hostname == "" {
			return "", errors.New("source: Git repository needs a hostname")
		}
		u.Host = net.JoinHostPort(o.hostname, o.port)
		if (o.scheme == "https" && o.port == "443") || (o.scheme == "http" && o.port == "80") {
			u.Host = o.hostname
			if strings.Contains(o.hostname, ":") {
				u.Host = "[" + o.hostname + "]"
			}
		}
	}
	return u.String(), nil
}

// Process-local HMAC prevents identities from exposing or enabling offline guesses
// of credentials. Neither raw credentials nor Secret names enter retained entries.
var gitAuthorizationKey = func() []byte {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		panic("source: cannot initialize Git authorization fingerprint")
	}
	return key
}()

func gitBaseIdentity(repository, sha string, auth transport.AuthMethod) string {
	access := "anonymous"
	if basic, ok := auth.(*httpauth.BasicAuth); ok {
		mac := hmac.New(sha256.New, gitAuthorizationKey)
		_, _ = fmt.Fprintf(mac, "%d:%s%d:%s", len(basic.Username), basic.Username, len(basic.Password), basic.Password)
		access = hex.EncodeToString(mac.Sum(nil))
	}
	sum := sha256.Sum256([]byte(repository + "\x00" + sha + "\x00" + access))
	return hex.EncodeToString(sum[:])
}

// gitCheckout owns an independent repository-root checkout. Call Cleanup on every
// success, after staging/rendering. Identity includes repository + SHA + current
// authorization and is safe for deterministic staging/cache keys (not a URL).
type gitCheckout struct {
	Root     string
	SHA      string
	Identity string
	Cleanup  func()
}

// acquire resolves credentials exactly once BEFORE resolving the ref or reading
// snapshots. Mutable refs are always listed anew; fetches use only the full SHA.
// It returns the WHOLE repository, not a selected operand subtree.
func (r *gitResolver) acquire(ctx context.Context, repository, ref string) (gitCheckout, error) {
	base, err := canonicalGitRepository(repository)
	if err != nil {
		return gitCheckout{}, err
	}
	if ref == "" {
		return gitCheckout{}, fmt.Errorf("source: missing pinned ref on %s", base)
	}
	auth, err := r.authFor(ctx, base)
	if err != nil {
		return gitCheckout{}, fmt.Errorf("source: repository %s: %w", base, err)
	}
	sha, err := resolveGitID(ctx, base, ref, auth)
	if err != nil {
		return gitCheckout{}, err
	}
	identity := gitBaseIdentity(base, sha, auth)
	if r.bases != nil {
		if err := r.bases.lock(ctx); err != nil {
			return gitCheckout{}, err
		}
		defer r.bases.unlock()
	}
	if err := ctx.Err(); err != nil {
		return gitCheckout{}, err
	}
	dir, err := os.MkdirTemp(r.scratchDir, "ddo-kustomize-")
	if err != nil {
		return gitCheckout{}, err
	}
	checkout := gitCheckout{Root: dir, SHA: sha, Identity: identity, Cleanup: func() { _ = os.RemoveAll(dir) }}
	success := false
	defer func() {
		if !success {
			checkout.Cleanup()
		}
	}()
	if data, ok := r.bases.get(identity); ok {
		err = restoreGitSnapshot(ctx, dir, data)
	} else {
		if err = fetchSHA(ctx, dir, base, sha, auth); err != nil {
			return gitCheckout{}, gitAcquisitionError(ctx, "fetch SHA "+sha, base, err)
		}
		if r.bases != nil {
			var data []byte
			data, err = snapshotGitCheckout(ctx, dir, r.bases.limit)
			if err == nil {
				r.bases.put(identity, data)
			}
		}
	}
	if err != nil {
		return gitCheckout{}, err
	}
	if err := ctx.Err(); err != nil {
		return gitCheckout{}, err
	}
	success = true
	return checkout, nil
}

func gitAcquisitionError(ctx context.Context, operation, base string, cause error) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("source: %s on %s: %w", operation, base, err)
	}
	// Transport/server errors can echo Authorization values or credential-bearing
	// redirects. Do not return them; repository + operation identify the failure.
	if errors.Is(cause, transport.ErrAuthenticationRequired) || errors.Is(cause, transport.ErrAuthorizationFailed) {
		return fmt.Errorf("source: %s on %s: authentication failed (fail-closed)", operation, base)
	}
	return fmt.Errorf("source: %s on %s failed (fail-closed)", operation, base)
}
