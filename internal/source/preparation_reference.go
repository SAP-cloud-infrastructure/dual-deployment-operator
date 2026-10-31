// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"errors"
	"net/url"
	"path"
	"regexp"
	"strings"
)

// gitBaseReference is the deliberately bounded, public-API-only counterpart of
// Kustomize's internal Git parser. HTTPS repositories require ?ref=; subdirectories
// require //, except GitHub shorthand which also accepts org/repo/subdirectory.
// No transport options, embedded credentials, fragments, or implicit default refs.
type gitBaseReference struct{ Repository, SubPath, Ref string }

var remoteBasePrefix = regexp.MustCompile(`(?i)^(?:[a-z][a-z0-9+.-]*:|[^/\s]+@[^/\s]+[:/]|github\.com:|//)`)

func classifyGitBase(raw string) (gitBaseReference, bool, error) {
	var result gitBaseReference
	shorthand := strings.HasPrefix(strings.ToLower(raw), "github.com/")
	if !shorthand && !remoteBasePrefix.MatchString(raw) {
		return result, false, nil
	}
	if shorthand {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return result, true, errors.New("unsupported remote base URL (HTTPS without credentials required)")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["ref"]) != 1 || strings.TrimSpace(q.Get("ref")) == "" {
		return result, true, errors.New("remote base requires exactly one nonempty ref and no other URL options")
	}
	repo, sub, explicit := strings.Cut(strings.TrimPrefix(u.Path, "/"), "//")
	if shorthand && !explicit {
		segments := strings.SplitN(repo, "/", 3)
		if len(segments) == 3 {
			repo, sub = strings.Join(segments[:2], "/"), segments[2]
		}
	}
	if repo == "" || strings.ContainsAny(repo+sub, "\\\x00\r\n") || strings.Contains(repo, "/_git/") {
		return result, true, errors.New("unsupported remote repository path")
	}
	for segment := range strings.SplitSeq(repo, "/") {
		if segment == "" || segment == "." || segment == ".." {
			return result, true, errors.New("invalid remote repository path")
		}
	}
	// Without an explicit separator, Kustomize defaults to two repository path
	// segments unless .git terminates the repository. Reject ambiguous variants.
	if !explicit && !shorthand && strings.Count(repo, "/") != 1 && !strings.HasSuffix(repo, ".git") {
		return result, true, errors.New("ambiguous repository URL: use an explicit // subdirectory separator")
	}
	if i := strings.Index(repo, ".git"); i >= 0 && i != len(repo)-4 {
		return result, true, errors.New("ambiguous .git repository delimiter")
	}
	switch strings.ToLower(path.Ext(repo)) {
	case ".yaml", ".yml", ".json":
		return result, true, errors.New("remote file loads are unsupported")
	}
	cleanSub := path.Clean(sub)
	if strings.HasPrefix(sub, "/") || cleanSub == ".." || strings.HasPrefix(cleanSub, "../") {
		return result, true, errors.New("remote subdirectory escapes repository")
	}
	u.Host = strings.ToLower(u.Host)
	u.Path, u.RawQuery, u.ForceQuery = "/"+repo, "", false
	result = gitBaseReference{Repository: u.String(), SubPath: cleanSub, Ref: q.Get("ref")}
	return result, true, nil
}
