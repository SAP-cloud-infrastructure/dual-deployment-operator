// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

// Tests for authenticated git credential threading.
//
// TestGitResolverHTTPSBasicAuth: end-to-end approach.
//   go-git's BasicAuth.SetAuth calls r.SetBasicAuth(user,pass) eagerly on every
//   outbound HTTP request (common.go:358). An httptest.Server is sufficient to
//   observe the Authorization header without needing a real git server: the test
//   records whether the header arrived with the correct credentials, then returns
//   a 403 to short-circuit the fetch. The key assertion is that the resolver
//   attached the credentials derived from the resolved creds, proving the chain:
//     creds{user,pass,ok:true} → authFor → *httpauth.BasicAuth → SetAuth → header.
//   A no-leak assertion verifies the password does not appear in the returned error.
//
// TestFromThreadsHelmCredsToLoader: unit-tests that From() threads credentials all
//   the way into the concrete *helmLoader so that its .resolve closure returns the
//   expected creds (not just that From returns nil error).

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// TestGitResolverHTTPSBasicAuth proves that a gitResolver with resolved credentials
// attaches them as HTTP Basic auth on every outbound request to the git remote.
// Strategy: httptest.Server records the Authorization header; returns 403 to
// short-circuit the fetch without requiring a real git repo. We then assert:
//  1. The observed Authorization header matches base64("u:pw").
//  2. The error returned by Resolve does NOT contain the password string.
//  3. A resolver with no credentials sends NO Authorization header.
func TestGitResolverHTTPSBasicAuth(t *testing.T) {
	const (
		wantUser = "u"
		wantPass = "pw"
	)
	wantToken := "Basic " + base64.StdEncoding.EncodeToString([]byte(wantUser+":"+wantPass))

	// --- authed resolver ---
	var gotAuth atomic.Value // stores string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		http.Error(w, "not a git server", http.StatusForbidden)
	}))
	defer srv.Close()

	authedResolver := &gitResolver{
		resolve: func(_ context.Context, _ string) (creds, error) {
			return creds{user: wantUser, pass: wantPass, ok: true}, nil
		},
	}
	_, cleanup, err := authedResolver.Resolve(context.Background(), srv.URL+"?ref=v1", "")
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("expected an error (server returns 403), got nil")
	}

	obs := ""
	if v := gotAuth.Load(); v != nil {
		obs = v.(string)
	}
	if obs == "" {
		t.Fatal("authed resolver: httptest server received NO Authorization header; expected Basic credentials")
	}
	if obs != wantToken {
		t.Fatalf("authed resolver: Authorization header = %q, want %q", obs, wantToken)
	}

	// no-leak: password must not appear in the error message
	if strings.Contains(err.Error(), wantPass) {
		t.Fatalf("credential leak: password %q found in error string %q", wantPass, err.Error())
	}

	// --- anonymous resolver: no Authorization header ---
	var anonAuth atomic.Value
	srvAnon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		anonAuth.Store(r.Header.Get("Authorization"))
		http.Error(w, "not a git server", http.StatusForbidden)
	}))
	defer srvAnon.Close()

	anonResolver := &gitResolver{} // nil resolve => anonymous
	_, cleanupAnon, _ := anonResolver.Resolve(context.Background(), srvAnon.URL+"?ref=v1", "")
	if cleanupAnon != nil {
		cleanupAnon()
	}

	if v := anonAuth.Load(); v != nil && v.(string) != "" {
		t.Fatalf("anonymous resolver: unexpected Authorization header %q; want none", v.(string))
	}
}

// TestFromThreadsHelmCredsToLoader proves that From() creates a *helmLoader whose
// resolve closure returns the credentials threaded from the CredentialResolver and
// the authSecretRef — not merely that From returns nil error.
func TestFromThreadsHelmCredsToLoader(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "mysecret", Namespace: "ns"},
		Data:       map[string][]byte{"token": []byte("tok")},
	}).Build()

	deps := Deps{
		ChartLoader:        NewHelmLoader(),
		CredentialResolver: &CredentialResolver{Client: cl, Namespace: "ns"},
	}
	spec := v1alpha1.Source{Helm: &v1alpha1.HelmSource{
		Repo: "oci://r", Name: "n", Version: "1",
		AuthSecretRef: &v1alpha1.SecretReference{Name: "mysecret"},
	}}

	src, err := From(spec, deps)
	if err != nil {
		t.Fatalf("From: %v", err)
	}
	hs, ok := src.(*helmSource)
	if !ok {
		t.Fatalf("From returned %T, want *helmSource", src)
	}
	hl, ok := hs.loader.(*helmLoader)
	if !ok {
		t.Fatalf("helmSource.loader is %T, want *helmLoader", hs.loader)
	}
	if hl.resolve == nil {
		t.Fatal("helmSource.loader.resolve is nil; credentials were not threaded")
	}

	got, err := hl.resolve(context.Background(), "")
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	// token key => user defaults to "git", pass = token value
	if got.user != "git" || got.pass != "tok" || !got.ok {
		t.Fatalf("resolve returned creds{user=%q pass=%q ok=%v}, want {user=git pass=tok ok=true}",
			got.user, got.pass, got.ok)
	}
}
