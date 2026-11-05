// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestIdleReadConn_ReadTimesOutOnStalledPeer verifies that idleReadConn bounds a
// stalled read: when the peer never writes, each Read refreshes a per-read
// deadline (SetReadDeadline) and the call returns a timeout error within the
// idle bound rather than blocking forever. This is the socket-level control
// that bounds a mid-body stall from krusty's remote-file loader (which uses
// http.DefaultTransport — see HardenDefaultHTTPTransport).
func TestIdleReadConn_ReadTimesOutOnStalledPeer(t *testing.T) {
	// net.Pipe gives us a connected pair with no buffering; the server side
	// never writes, so a Read on the client side blocks until the deadline.
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	const idle = 40 * time.Millisecond
	c := &idleReadConn{Conn: clientConn, idle: idle}

	done := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := c.Read(buf)
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a timeout error from stalled Read, got nil")
		}
		var netErr net.Error
		if !errors.As(err, &netErr) || !netErr.Timeout() {
			t.Fatalf("expected a net.Error with Timeout()==true, got %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idleReadConn.Read did not return within the idle bound; it blocked")
	}
}

// TestIdleReadConn_ReadSucceedsWhenPeerWrites is the positive control: when the
// peer writes promptly, the per-read deadline does not fire and the data is
// returned normally.
func TestIdleReadConn_ReadSucceedsWhenPeerWrites(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	t.Cleanup(func() {
		_ = clientConn.Close()
		_ = serverConn.Close()
	})

	const idle = 2 * time.Second
	c := &idleReadConn{Conn: clientConn, idle: idle}

	want := []byte("hello")
	writeErr := make(chan error, 1)
	go func() {
		_, err := serverConn.Write(want)
		writeErr <- err
	}()

	buf := make([]byte, len(want))
	n, err := io.ReadFull(c, buf)
	if err != nil {
		t.Fatalf("expected successful read, got %v", err)
	}
	if string(buf[:n]) != string(want) {
		t.Fatalf("read %q, want %q", buf[:n], want)
	}
	if err := <-writeErr; err != nil {
		t.Fatalf("peer write failed: %v", err)
	}
}

// TestHardenDefaultHTTPTransport_SetsBoundedFields asserts the hardening applies
// the expected timeouts to http.DefaultTransport and forces HTTP/1.1. The
// original transport is saved and restored so this test does not pollute other
// tests in the package that may rely on http.DefaultTransport.
func TestHardenDefaultHTTPTransport_SetsBoundedFields(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	HardenDefaultHTTPTransport()

	tr, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		t.Fatalf("http.DefaultTransport is not *http.Transport after hardening: %T", http.DefaultTransport)
	}
	if tr.ResponseHeaderTimeout != 15*time.Second {
		t.Errorf("ResponseHeaderTimeout = %v, want 15s", tr.ResponseHeaderTimeout)
	}
	if tr.TLSHandshakeTimeout != 10*time.Second {
		t.Errorf("TLSHandshakeTimeout = %v, want 10s", tr.TLSHandshakeTimeout)
	}
	if tr.IdleConnTimeout != 90*time.Second {
		t.Errorf("IdleConnTimeout = %v, want 90s", tr.IdleConnTimeout)
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("ForceAttemptHTTP2 = true, want false (HTTP/1.1 forced so connection-level deadlines are safe)")
	}
	if tr.DialContext == nil {
		t.Error("DialContext is nil, want the idleReadConn-wrapping dialer")
	}
	// TLSNextProto must be a non-nil empty map to disable HTTP/2 upgrade.
	if tr.TLSNextProto == nil {
		t.Error("TLSNextProto is nil, want an empty map to disable HTTP/2")
	}
}

// TestHardenDefaultHTTPTransport_PromptServerSucceeds verifies a well-behaved
// server still works through the hardened transport end-to-end: the hardening
// must not break normal, prompt responses.
func TestHardenDefaultHTTPTransport_PromptServerSucceeds(t *testing.T) {
	orig := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = orig })

	const body = "ok-body"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := io.WriteString(w, body); err != nil {
			t.Errorf("server failed to write body: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	HardenDefaultHTTPTransport()

	// Use a client that explicitly routes through the hardened DefaultTransport.
	client := &http.Client{Transport: http.DefaultTransport}
	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("GET through hardened transport failed: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	got, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("reading body failed: %v", err)
	}
	if string(got) != body {
		t.Fatalf("body = %q, want %q", got, body)
	}
}
