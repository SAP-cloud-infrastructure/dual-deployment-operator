// SPDX-FileCopyrightText: 2026 SAP SE or an SAP affiliate company
// Copyright 2026.
//
// SPDX-License-Identifier: Apache-2.0

package source

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1alpha1 "github.com/SAP-cloud-infrastructure/dual-deployment-operator/api/v1alpha1"
)

// creds is a resolved credential. ok=false means anonymous.
type creds struct {
	user string
	pass string
	ok   bool
}

// credsFromSecretData maps fixed keys to a credential. token wins over password;
// a token with no username defaults username to "git" (git HTTPS PAT convention).
func credsFromSecretData(data map[string][]byte) creds {
	user := string(data["username"])
	pass := string(data["password"])
	if tok := string(data["token"]); tok != "" {
		pass = tok
		if user == "" {
			user = "git"
		}
	}
	return creds{user: user, pass: pass, ok: user != "" || pass != ""}
}

// CredentialResolver reads an authSecretRef Secret from the CR's namespace.
type CredentialResolver struct {
	Client    client.Client
	Namespace string
}

// Resolve reads the Secret named by ref (nil ref => anonymous).
func (r *CredentialResolver) Resolve(ctx context.Context, ref *v1alpha1.SecretReference) (creds, error) {
	if ref == nil || ref.Name == "" {
		return creds{}, nil
	}
	var s corev1.Secret
	if err := r.Client.Get(ctx, types.NamespacedName{Namespace: r.Namespace, Name: ref.Name}, &s); err != nil {
		return creds{}, err
	}
	return credsFromSecretData(s.Data), nil
}
