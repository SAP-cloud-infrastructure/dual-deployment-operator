// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"errors"
	"net/http"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func init() {
	// Install once during package initialization, never mutate go-git's global
	// protocol registry during concurrent reconciles. net/http's default redirect
	// policy allows credentials across ports/subdomains; Git can also reuse auth
	// after updating a redirected endpoint. Reject cross-origin redirects entirely.
	// This affects only Git HTTP transports, not Helm's HTTP clients.
	client := githttp.NewClient(&http.Client{Transport: http.DefaultTransport, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("source: too many Git redirects")
		}
		origin, err := gitOriginOf(via[0].URL.String())
		if err != nil {
			return err
		}
		target, err := gitOriginOf(req.URL.String())
		if err != nil {
			return err
		}
		if origin != target {
			return errors.New("source: cross-origin Git redirect rejected")
		}
		return nil
	}})
	gitclient.InstallProtocol("https", client)
	gitclient.InstallProtocol("http", client)
}
