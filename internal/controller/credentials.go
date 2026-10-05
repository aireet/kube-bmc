// Package controller executes BMCAction objects.
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/internal/oob"
)

// Credentials resolves out-of-band credentials for a BMC. Secrets are read only from
// Namespace; the server is not granted cluster-wide Secret access.
type Credentials struct {
	Reader    client.Reader
	Namespace string
	// Default is the Secret used when a BMC has no spec.credentialsRef.
	Default string
}

// For returns the credentials configured for b.
func (c Credentials) For(ctx context.Context, b *bmcv1.BMC) (oob.Credentials, error) {
	name := c.Default
	if ref := b.Spec.CredentialsRef; ref != nil && ref.Name != "" {
		name = ref.Name
	}
	if name == "" {
		return oob.Credentials{}, fmt.Errorf("no credentials configured for BMC %s", b.Name)
	}
	s := &corev1.Secret{}
	if err := c.Reader.Get(ctx, client.ObjectKey{Namespace: c.Namespace, Name: name}, s); err != nil {
		return oob.Credentials{}, fmt.Errorf("credentials secret %s/%s: %w", c.Namespace, name, err)
	}
	creds := oob.Credentials{Username: string(s.Data["username"]), Password: string(s.Data["password"])}
	if creds.Username == "" {
		return creds, fmt.Errorf("secret %s/%s has no username key", c.Namespace, name)
	}
	return creds, nil
}
