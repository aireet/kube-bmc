// Package controller executes BMCAction objects.
package controller

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	bmcv1 "github.com/aireet/kube-bmc/api/v1alpha1"
	"github.com/aireet/kube-bmc/bmc"
)

// Endpoints resolves how the server reaches a BMC over the network. Credentials are read
// only from Secrets in Namespace; the server is not granted cluster-wide Secret access.
type Endpoints struct {
	Reader    client.Reader
	Namespace string
	// DefaultSecret holds the credentials of BMCs without spec.credentialsRef.
	DefaultSecret string
}

// For returns the endpoint of b. spec.address takes precedence over the address the
// node agent discovered in-band.
func (e Endpoints) For(ctx context.Context, b *bmcv1.BMC) (bmc.Endpoint, error) {
	addr := b.Spec.Address
	if addr == "" {
		addr = b.Status.Network.IPAddress
	}
	if addr == "" {
		return bmc.Endpoint{}, fmt.Errorf("BMC %s has no address: set spec.address or wait for the agent to discover it", b.Name)
	}
	name := e.DefaultSecret
	if ref := b.Spec.CredentialsRef; ref != nil && ref.Name != "" {
		name = ref.Name
	}
	if name == "" {
		return bmc.Endpoint{}, fmt.Errorf("no credentials configured for BMC %s", b.Name)
	}
	s := &corev1.Secret{}
	if err := e.Reader.Get(ctx, client.ObjectKey{Namespace: e.Namespace, Name: name}, s); err != nil {
		return bmc.Endpoint{}, fmt.Errorf("credentials secret %s/%s: %w", e.Namespace, name, err)
	}
	if len(s.Data["username"]) == 0 {
		return bmc.Endpoint{}, fmt.Errorf("secret %s/%s has no username key", e.Namespace, name)
	}
	return bmc.Endpoint{
		Address:  addr,
		Protocol: bmc.Protocol(b.Spec.Protocol),
		Username: string(s.Data["username"]),
		Password: string(s.Data["password"]),
		Insecure: b.Spec.InsecureSkipVerify,
	}, nil
}
