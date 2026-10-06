// Package redfish controls the power of a server through the Redfish service of its BMC.
//
// It covers what out-of-band power management needs, on top of the general Redfish
// client github.com/stmcginnis/gofish:
//
//	c, err := redfish.Dial(ctx, redfish.Config{Endpoint: "10.0.0.7", Username: "admin", Password: pw, Insecure: true})
//	if err != nil {
//		return err
//	}
//	defer c.Close()
//	return c.Reset(ctx, redfish.PowerCycle)
package redfish

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/stmcginnis/gofish"
	"github.com/stmcginnis/gofish/schemas"
)

// Config describes how to reach a Redfish service.
type Config struct {
	// Endpoint is the URL of the BMC, or its host; https is assumed without a scheme.
	Endpoint string
	Username string
	Password string
	// Insecure skips verification of the BMC's TLS certificate, which is usually
	// self-signed.
	Insecure bool
	// Timeout bounds each HTTP request. Zero means 30 seconds.
	Timeout time.Duration
}

// Client is a connection to the Redfish service of one BMC. It acts on the first
// computer system the service lists, which is the server on single-node BMCs.
type Client struct {
	api      *gofish.APIClient
	endpoint string
}

// Dial connects to the Redfish service described by cfg.
func Dial(ctx context.Context, cfg Config) (*Client, error) {
	endpoint := cfg.Endpoint
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	api, err := gofish.ConnectContext(ctx, gofish.ClientConfig{
		Endpoint:            endpoint,
		Username:            cfg.Username,
		Password:            cfg.Password,
		Insecure:            cfg.Insecure,
		BasicAuth:           true,
		TLSHandshakeTimeout: 10,
		// gofish applies Insecure and the handshake timeout only to an *http.Transport.
		HTTPClient: &http.Client{Timeout: timeout, Transport: &http.Transport{Proxy: http.ProxyFromEnvironment}},
	})
	if err != nil {
		return nil, fmt.Errorf("redfish %s: connect: %w", cfg.Endpoint, err)
	}
	return &Client{api: api, endpoint: cfg.Endpoint}, nil
}

// Close ends the session.
func (c *Client) Close() error {
	c.api.Logout()
	return nil
}

// ResetType is a Redfish reset type. The constants are those every BMC is expected to
// support; others defined by the Redfish schema, such as "Nmi", can be converted from
// strings.
type ResetType string

const (
	On               ResetType = "On"
	ForceOff         ResetType = "ForceOff"
	GracefulShutdown ResetType = "GracefulShutdown"
	GracefulRestart  ResetType = "GracefulRestart"
	ForceRestart     ResetType = "ForceRestart"
	PowerCycle       ResetType = "PowerCycle"
)

// Reset resets the computer system.
func (c *Client) Reset(ctx context.Context, t ResetType) error {
	sys, err := c.system(ctx)
	if err != nil {
		return err
	}
	if _, err := sys.Reset(schemas.ResetType(t)); err != nil {
		return fmt.Errorf("redfish %s: reset %s: %w", c.endpoint, t, err)
	}
	return nil
}

// PowerState returns the power state of the computer system, such as "On" or "Off".
func (c *Client) PowerState(ctx context.Context) (string, error) {
	sys, err := c.system(ctx)
	if err != nil {
		return "", err
	}
	return string(sys.PowerState), nil
}

func (c *Client) system(ctx context.Context) (*schemas.ComputerSystem, error) {
	systems, err := c.api.WithContext(ctx).Service.Systems()
	if err != nil {
		return nil, fmt.Errorf("redfish %s: list systems: %w", c.endpoint, err)
	}
	if len(systems) == 0 {
		return nil, fmt.Errorf("redfish %s: %w", c.endpoint, errNoSystem)
	}
	return systems[0], nil
}

var errNoSystem = errors.New("no computer system found")
