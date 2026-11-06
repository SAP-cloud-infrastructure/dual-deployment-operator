// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"crypto/tls"
	"net"
	"net/http"
	"time"
)

// Default bounds for the hardened HTTP transport. These values are deliberately
// generous enough for a legitimate slow mirror but tight enough that a hung or
// slow-loris remote cannot block a reconcile indefinitely.
const (
	defaultDialTimeout           = 10 * time.Second
	defaultTLSHandshakeTimeout   = 10 * time.Second
	defaultResponseHeaderTimeout = 15 * time.Second
	// defaultReadIdleTimeout bounds a single stalled Read on the connection.
	// This is the ONLY mechanism that caps a server which sends response
	// headers promptly and then stalls mid-body (see HardenDefaultHTTPTransport
	// doc for why http.Transport has no body-read deadline field).
	defaultReadIdleTimeout = 30 * time.Second
	defaultIdleConnTimeout = 90 * time.Second
)

// idleReadConn wraps a net.Conn and resets an absolute read deadline before
// every Read. Because Go's deadlines persist until changed, refreshing the
// deadline on each Read turns it into a per-read (idle) timeout: any single
// Read that blocks longer than idle fails with a timeout error, which in turn
// fails the surrounding HTTP request.
//
// This is the recognized last-resort idiom for bounding a stalled connection
// when the caller cannot set http.Client.Timeout or a request context — exactly
// our situation, because kustomize's file loader constructs a bare
// &http.Client{} (sigs.k8s.io/kustomize/api/internal/loader/fileloader.go) that
// we cannot reach. It is an IDLE timeout, not a total-request timeout: a server
// dripping one byte just under idle forever is not caught here (that would
// require a client/context deadline we do not control).
type idleReadConn struct {
	net.Conn
	idle time.Duration
}

func (c *idleReadConn) Read(p []byte) (int, error) {
	if err := c.SetReadDeadline(time.Now().Add(c.idle)); err != nil {
		return 0, err
	}
	// NOTE: must call c.Conn.Read (not c.Read) — c.Read would recurse into this
	// method infinitely since Read is defined on *idleReadConn.
	return c.Conn.Read(p)
}

// HardenDefaultHTTPTransport installs connection/response timeouts onto
// http.DefaultTransport so that kustomize's remote-file loader (which builds a
// timeout-less &http.Client{} with a nil Transport, thereby falling back to
// http.DefaultTransport) cannot block a reconcile forever on an unresponsive
// remote.
//
// Why mutate the package global instead of using a fresh transport (the usual
// advice)? Because we do not own the http.Client — kustomize constructs it
// internally with no injection point. A client with a nil Transport uses
// http.DefaultTransport, so hardening that global is the only lever available.
//
// Because http.DefaultTransport is a process global, this ALSO affects other
// consumers that fall back to it:
//   - Helm's OCI registry client (helm/pkg/registry.NewClient) clones
//     http.DefaultTransport per call, so OCI chart pulls inherit the same
//     15 s header timeout, 30 s per-read idle deadline, and HTTP/1.1 forcing.
//     This is acceptable — and arguably beneficial — since it bounds a hung
//     registry. For large chart blobs the 30 s idle deadline is per-read, not
//     total-request, so normal pulls are not affected as long as data flows.
//   - go-git captures http.DefaultTransport at package init, BEFORE this call
//     runs; it is therefore unaffected.
//   - controller-runtime / kube-client builds its own transport via rest.Config;
//     it is unaffected.
//
// What this bounds:
//   - connection establishment      (net.Dialer.Timeout)
//   - TLS handshake                  (Transport.TLSHandshakeTimeout)
//   - waiting for response headers   (Transport.ResponseHeaderTimeout)
//   - a stalled mid-body read        (idleReadConn per-Read deadline)
//
// What it does NOT bound: a server that trickles the body just fast enough to
// stay under the idle deadline. Capping total body time requires a client-level
// Timeout or request context, neither of which kustomize exposes; this is the
// best containment achievable from our position and is documented as such.
//
// HTTP/2 is force-disabled on this transport process-wide for consumers that
// fall back to the default (including kustomize's file loader and Helm's OCI
// client). With HTTP/2 a connection-level read deadline would affect unrelated
// multiplexed streams, making the idleReadConn mechanism unsafe. For Helm OCI
// pulls this trades H2 multiplexing for the safety of the per-read deadline —
// an acceptable tradeoff given the timeouts are generous for blob pulls.
//
// The function is idempotent-safe to call once at process startup. It clones
// http.DefaultTransport (rather than mutating its fields in place) and then
// reassigns the global, so concurrent readers never observe a half-configured
// transport.
func HardenDefaultHTTPTransport() {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		// Non-standard transport already installed; leave it untouched rather
		// than clobber someone else's configuration.
		return
	}
	tr := base.Clone()

	dialer := &net.Dialer{
		Timeout:   defaultDialTimeout,
		KeepAlive: 30 * time.Second,
	}
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dialer.DialContext(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &idleReadConn{Conn: conn, idle: defaultReadIdleTimeout}, nil
	}
	tr.TLSHandshakeTimeout = defaultTLSHandshakeTimeout
	tr.ResponseHeaderTimeout = defaultResponseHeaderTimeout
	tr.IdleConnTimeout = defaultIdleConnTimeout

	// Force HTTP/1.1: a connection-level read deadline is unsafe under HTTP/2
	// multiplexing (it would cancel unrelated streams).
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = map[string]func(string, *tls.Conn) http.RoundTripper{}

	// Clearing TLSNextProto and ForceAttemptHTTP2 is NOT enough: the cloned
	// transport's TLSClientConfig still carries NextProtos=["h2","http/1.1"], so
	// ALPN keeps offering "h2". An HTTP/2-capable server (ghcr.io,
	// raw.githubusercontent.com, Keppel, registry-1.docker.io, quay.io) then
	// negotiates h2, Go finds no h2 handler in the empty TLSNextProto map, writes
	// an HTTP/1.1 request onto the h2 connection, and the response comes back as
	// "malformed HTTP response"/EOF. Pin the ALPN offer to http/1.1 so h2 is
	// never negotiated in the first place.
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}
	tr.TLSClientConfig.NextProtos = []string{"http/1.1"}

	http.DefaultTransport = tr
}
