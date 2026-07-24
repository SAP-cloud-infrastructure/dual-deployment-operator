// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import "testing"

func TestCredentialsFromSecretData(t *testing.T) {
	// token wins over password
	c := credsFromSecretData(map[string][]byte{
		"username": []byte("u"), "password": []byte("p"), "token": []byte("tok"),
	})
	if c.user != "u" || c.pass != "tok" {
		t.Fatalf("token must win: got user=%q pass=%q", c.user, c.pass)
	}
	// token only => username defaults to "git" (for git HTTPS PAT)
	c = credsFromSecretData(map[string][]byte{"token": []byte("tok")})
	if c.user != "git" || c.pass != "tok" {
		t.Fatalf("token-only: got user=%q pass=%q", c.user, c.pass)
	}
	// basic auth
	c = credsFromSecretData(map[string][]byte{"username": []byte("u"), "password": []byte("p")})
	if c.user != "u" || c.pass != "p" || !c.ok {
		t.Fatalf("basic: got %+v", c)
	}
	// empty => not ok (anonymous)
	if credsFromSecretData(map[string][]byte{}).ok {
		t.Fatal("empty secret data must resolve to anonymous (ok=false)")
	}
}
