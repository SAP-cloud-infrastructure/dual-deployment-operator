// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// SPDX-License-Identifier: Apache-2.0

// Package clients builds seed (in-cluster) and shoot (token+CA-from-Secret) clients.
package clients

import (
	"errors"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// shootRequestTimeout bounds every request to the shoot apiserver so an unreachable
// shoot fails fast and requeues instead of hanging the reconcile indefinitely.
const shootRequestTimeout = 30 * time.Second

// ErrShootCredentialsNotReady signals the token-requestor Secret exists but Gardener
// has not populated token/CA yet (absent or empty). Benign; caller maps it to a wait.
var ErrShootCredentialsNotReady = errors.New("shoot credentials not yet populated")

// SeedClient returns a client for the seed using the manager's REST config.
func SeedClient(cfg *rest.Config, opts client.Options) (client.Client, error) {
	return client.New(cfg, opts)
}

// ShootRESTConfig builds a shoot rest.Config from a token-requestor Secret. Token and
// CA must both be present and non-empty; otherwise ErrShootCredentialsNotReady.
func ShootRESTConfig(secret *corev1.Secret, tokenKey, caKey, server string) (*rest.Config, error) {
	if tokenKey == "" {
		tokenKey = "token"
	}
	if caKey == "" {
		caKey = "bundle.crt"
	}
	token := secret.Data[tokenKey]
	caData := secret.Data[caKey]
	if len(token) == 0 || len(caData) == 0 {
		return nil, ErrShootCredentialsNotReady
	}
	return &rest.Config{
		Host:            server,
		BearerToken:     string(token),
		TLSClientConfig: rest.TLSClientConfig{CAData: caData},
		Timeout:         shootRequestTimeout,
	}, nil
}
