// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"
	"errors"
	"testing"
	"time"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// blockingRootResolver blocks in Resolve until the context is cancelled, then
// returns ctx.Err(). It models a remote endpoint that accepts the connection
// but never responds, so the only thing that can unblock the resolve is the
// per-render deadline. Honoring ctx is exactly what the production go-git
// resolver and the git subprocess krusty shells out to both do.
type blockingRootResolver struct {
	entered chan struct{} // closed once Resolve has started blocking
}

func (b *blockingRootResolver) Resolve(ctx context.Context, _, _ string) (fsPath string, cleanup func(), err error) {
	if b.entered != nil {
		close(b.entered)
	}
	<-ctx.Done()
	return "", func() {}, ctx.Err()
}

// TestRenderTimeoutCancelsStalledResolve proves that a kustomizeSource with a
// non-zero renderTimeout bounds a stalled root resolve: when the resolver never
// returns on its own, Render returns once the deadline elapses and the error
// chain carries context.DeadlineExceeded. This is the community-norm behavior
// (Flux spec.timeout / Argo ARGOCD_EXEC_TIMEOUT) that frees the reconcile
// worker instead of stalling it indefinitely.
func TestRenderTimeoutCancelsStalledResolve(t *testing.T) {
	entered := make(chan struct{})
	k := &kustomizeSource{
		spec:          &v1alpha1.KustomizeSource{URL: "git::https://example.com/org/repo//root?ref=main"},
		resolver:      &blockingRootResolver{entered: entered},
		renderTimeout: 50 * time.Millisecond,
	}

	done := make(chan error, 1)
	go func() {
		_, err := k.Render(context.Background(), ModeSeed, "ns")
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve was never entered")
	}

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected a deadline error, got nil")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("expected context.DeadlineExceeded in error chain, got: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Render did not return after the render timeout elapsed")
	}
}

// TestRenderTimeoutZeroImposesNoDeadline proves the opt-out: with renderTimeout
// == 0 the render carries no deadline, so a resolve that would outlive any
// small timeout is not cancelled by Render itself. The caller's context is the
// only bound. We assert the resolve is still blocked (never cancelled) a short
// while after it starts.
func TestRenderTimeoutZeroImposesNoDeadline(t *testing.T) {
	entered := make(chan struct{})
	k := &kustomizeSource{
		spec:          &v1alpha1.KustomizeSource{URL: "git::https://example.com/org/repo//root?ref=main"},
		resolver:      &blockingRootResolver{entered: entered},
		renderTimeout: 0,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		_, err := k.Render(ctx, ModeSeed, "ns")
		done <- err
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("Resolve was never entered")
	}

	// Render must NOT return on its own: with no deadline the only thing that
	// can unblock the resolver is the caller cancelling ctx.
	select {
	case err := <-done:
		t.Fatalf("Render returned unexpectedly without a deadline: %v", err)
	case <-time.After(150 * time.Millisecond):
		// Still blocked, as expected.
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Render did not return after the caller cancelled ctx")
	}
}
