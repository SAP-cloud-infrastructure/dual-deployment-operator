// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

package equivalence

import (
	"context"
	"crypto/sha256"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/cgi" //nolint:gosec // G504: test-only loopback Git backend; supported Go versions include the Go 1.6.3 Httpoxy fix.
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-git/go-git/v5/plumbing/protocol/packp"
	"github.com/go-git/go-git/v5/plumbing/transport"

	gitclient "github.com/go-git/go-git/v5/plumbing/transport/client"
	githttp "github.com/go-git/go-git/v5/plumbing/transport/http"
)

func TestIPAMGitTransportDelegation(t *testing.T) {
	localErr, remoteErr := errors.New("local transport"), errors.New("remote rejection")
	local := &ipamRecordingTransport{err: localErr}
	remote := &ipamRecordingTransport{err: remoteErr}
	router := &ipamGitTransport{localEndpoint: "https://127.0.0.1:12345/candidate.git", local: local, remote: remote}
	for _, raw := range []string{
		router.localEndpoint,
		"https://github.com/kubernetes-sigs/cluster-api-ipam-provider-in-cluster",
		"https://127.0.0.1:12346/candidate.git",
		"https://127.0.0.1:12345/other.git",
		"https://127.0.0.1:12345/candidate.git/extra",
		"http://127.0.0.1:12345/candidate.git",
		"https://localhost:12345/candidate.git",
	} {
		t.Run(raw, func(t *testing.T) {
			ep, err := transport.NewEndpoint(raw)
			if err != nil {
				t.Fatal(err)
			}
			auth := &githttp.BasicAuth{Username: "fixture"}
			selected, expectedErr := remote, remoteErr
			if raw == router.localEndpoint {
				selected, expectedErr = local, localErr
			}
			for _, receive := range []bool{false, true} {
				local.calls, remote.calls = 0, 0
				if receive {
					_, err = router.NewReceivePackSession(ep, auth)
				} else {
					_, err = router.NewUploadPackSession(ep, auth)
				}
				if !errors.Is(err, expectedErr) || selected.calls != 1 || local.calls+remote.calls != 1 || selected.endpoint != ep || selected.auth != auth {
					t.Fatalf("receive=%v: endpoint/auth/error not delegated unchanged: %v", receive, err)
				}
			}
		})
	}
}

// Uses the saved production HTTPS transport, not an imitation redirect guard.
// The endpoint CA trusts only the loopback origin; the cross-origin target must
// never receive a request. No public network or global transport edits needed.
func TestIPAMGitTransportPreservesRedirectRejection(t *testing.T) {
	var targetRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		http.Error(w, "redirect should not arrive", http.StatusBadGateway)
	}))
	defer target.Close()
	var originRequests atomic.Int32
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		originRequests.Add(1)
		http.Redirect(w, r, target.URL+"/remote.git/info/refs", http.StatusFound)
	}))
	defer origin.Close()
	previous := gitclient.Protocols["https"]
	local := &ipamRecordingTransport{err: errors.New("local route unexpectedly used")}
	router := &ipamGitTransport{localEndpoint: "https://127.0.0.1:1/candidate.git", local: local, remote: previous}
	for _, client := range []transport.Transport{previous, router} {
		ep, err := transport.NewEndpoint(origin.URL + "/remote.git")
		if err != nil {
			t.Fatal(err)
		}
		ep.CaBundle = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: origin.Certificate().Raw})
		session, err := client.NewUploadPackSession(ep, nil)
		if err != nil {
			t.Fatal(err)
		}
		_, requestErr := session.AdvertisedReferencesContext(context.Background())
		if err := session.Close(); err != nil {
			t.Fatal(err)
		}
		if requestErr == nil || !strings.Contains(requestErr.Error(), "source: cross-origin Git redirect rejected") {
			t.Fatalf("production redirect rejection lost: %v", requestErr)
		}
	}
	if originRequests.Load() != 2 || targetRequests.Load() != 0 || local.calls != 0 {
		t.Fatalf("origin=%d target=%d local=%d", originRequests.Load(), targetRequests.Load(), local.calls)
	}
}

type ipamRecordingTransport struct {
	endpoint *transport.Endpoint
	auth     transport.AuthMethod
	err      error
	calls    int
}

func (r *ipamRecordingTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	r.endpoint, r.auth, r.calls = ep, auth, r.calls+1
	return nil, r.err
}

func (r *ipamRecordingTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	r.endpoint, r.auth, r.calls = ep, auth, r.calls+1
	return nil, r.err
}

const (
	ipamCandidateSHA  = "463740c01f7466f360847ae6507ff06570903aac"
	ipamCandidatePath = "system/kustomize/ipam-capi-remote-ddo"
)

// stageIPAMCandidate serves the exact published commit without source changes.
// Vendored core files are verified, never downloaded/replaced. The provider
// directory stays remote, so real NewGitResolver acquisition is required.
func stageIPAMCandidate(t *testing.T, ctx context.Context, originalURL string) string {
	t.Helper()
	if originalURL != "https://github.com/sapcc/helm-charts//"+ipamCandidatePath+"?ref="+ipamCandidateSHA {
		t.Fatal("candidate fixture provenance changed")
	}
	root := t.TempDir()
	ipamGit(t, ctx, root, "init", "-q")
	ipamGit(t, ctx, root, "fetch", "--depth=1", "https://github.com/sapcc/helm-charts.git", ipamCandidateSHA)
	ipamGit(t, ctx, root, "checkout", "-q", "--detach", "FETCH_HEAD")
	if got := ipamGit(t, ctx, root, "rev-parse", "HEAD"); got != ipamCandidateSHA {
		t.Fatalf("candidate checkout = %s", got)
	}
	dir := filepath.Join(root, ipamCandidatePath, "shoot/core-crds")
	for file, digest := range map[string]string{
		"cluster.x-k8s.io_clusters.yaml":             "39768d1dbe14c7932e4fc4aacd90fcc08395f5922f74886e1a3d87868ce8d997",
		"ipam.cluster.x-k8s.io_ipaddresses.yaml":     "d5068e3d0031f0cca9514492712384d4325b2481d3e6854da1192dc6d88c28cd",
		"ipam.cluster.x-k8s.io_ipaddressclaims.yaml": "28b61b0fc4c4a66ed2d9ca70c488eae210251662a7d2bcb5cc3ad93c46e6fa19",
	} {
		content, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if got := fmt.Sprintf("%x", sha256.Sum256(content)); got != digest {
			t.Fatalf("vendored pinned bytes changed for %s: %s", file, got)
		}
	}
	if status := ipamGit(t, ctx, root, "status", "--porcelain"); status != "" {
		t.Fatalf("published candidate checkout changed: %s", status)
	}
	t.Logf("unmodified published candidate: commit=%s tree=%s; three vendored byte hashes verified", ipamCandidateSHA, ipamGit(t, ctx, root, "rev-parse", "HEAD^{tree}"))
	// Advertise that SAME commit to go-git; no new commit or source adaptation.
	ipamGit(t, ctx, root, "branch", "reviewed", ipamCandidateSHA)
	backend := filepath.Join(ipamGit(t, ctx, root, "--exec-path"), "git-http-backend")
	if _, err := os.Stat(backend); err != nil {
		t.Fatal("git-http-backend required: ", err)
	}
	parent := t.TempDir()
	ipamGit(t, ctx, parent, "clone", "--bare", root, filepath.Join(parent, "candidate.git"))
	handler := &cgi.Handler{Path: backend, Env: []string{"GIT_PROJECT_ROOT=" + parent, "GIT_HTTP_EXPORT_ALL=1"}}
	server := httptest.NewTLSServer(handler)
	var providerFetches atomic.Int32
	previous := gitclient.Protocols["https"]
	// The exception is one exact Git endpoint. Public sessions retain the saved
	// production transport, including its CheckRedirect policy and auth handling.
	gitclient.InstallProtocol("https", &ipamGitTransport{
		localEndpoint: server.URL + "/candidate.git",
		local:         githttp.NewClient(server.Client()), remote: previous,
		providerFetches: &providerFetches,
	})
	t.Cleanup(func() {
		gitclient.InstallProtocol("https", previous)
		server.Close()
		t.Logf("observed real public provider Git content fetches: %d", providerFetches.Load())
		if providerFetches.Load() == 0 {
			t.Error("real remote provider Git fetch was not observed")
		}
	})
	return server.URL + "/candidate.git//" + ipamCandidatePath + "?ref=" + ipamCandidateSHA
}

type ipamGitTransport struct {
	localEndpoint   string
	local, remote   transport.Transport
	providerFetches *atomic.Int32
}

func (g *ipamGitTransport) NewUploadPackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.UploadPackSession, error) {
	if ep.String() == g.localEndpoint {
		return g.local.NewUploadPackSession(ep, auth)
	}
	session, err := g.remote.NewUploadPackSession(ep, auth)
	if err != nil {
		return session, err
	}
	if g.providerFetches != nil && ep.Host == "github.com" && strings.TrimSuffix(ep.Path, ".git") == "/kubernetes-sigs/cluster-api-ipam-provider-in-cluster" {
		return &ipamObservedUploadPack{UploadPackSession: session, fetches: g.providerFetches}, nil
	}
	return session, nil
}

func (g *ipamGitTransport) NewReceivePackSession(ep *transport.Endpoint, auth transport.AuthMethod) (transport.ReceivePackSession, error) {
	if ep.String() == g.localEndpoint {
		return g.local.NewReceivePackSession(ep, auth)
	}
	return g.remote.NewReceivePackSession(ep, auth)
}

type ipamObservedUploadPack struct {
	transport.UploadPackSession
	fetches *atomic.Int32
}

func (s *ipamObservedUploadPack) UploadPack(ctx context.Context, req *packp.UploadPackRequest) (*packp.UploadPackResponse, error) {
	response, err := s.UploadPackSession.UploadPack(ctx, req)
	if err == nil {
		s.fetches.Add(1)
	}
	return response, err
}

func ipamGit(t *testing.T, ctx context.Context, dir string, args ...string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}
