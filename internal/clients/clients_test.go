// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
//
// SPDX-License-Identifier: Apache-2.0

package clients

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestBuildShootRestConfigCredentialsNotReady(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{"token": {}, "bundle.crt": []byte("CA")}}
	_, err := ShootRESTConfig(secret, "token", "bundle.crt", "https://api:443")
	if !errors.Is(err, ErrShootCredentialsNotReady) {
		t.Errorf("err = %v, want ErrShootCredentialsNotReady", err)
	}
}

func TestBuildShootRestConfigReady(t *testing.T) {
	secret := &corev1.Secret{Data: map[string][]byte{"token": []byte("tok"), "bundle.crt": []byte("CA")}}
	cfg, err := ShootRESTConfig(secret, "token", "bundle.crt", "https://api:443")
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if cfg.Host != "https://api:443" || cfg.BearerToken != "tok" || string(cfg.CAData) != "CA" {
		t.Errorf("cfg = %+v", cfg)
	}
}
