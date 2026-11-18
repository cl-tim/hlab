package health

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

// Endpoint is a service probe run from the machine invoking hlab.
type Endpoint struct {
	Name   string `yaml:"name"`
	Kind   string `yaml:"kind"`
	Target string `yaml:"target"`
}

func (e Endpoint) Probe(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	switch e.Kind {
	case "http":
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.Target, nil)
		if err != nil {
			return fmt.Errorf("invalid HTTP target")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return fmt.Errorf("HTTP request failed")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("HTTP status %d", resp.StatusCode)
		}
		return nil
	case "tcp":
		conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", e.Target)
		if err != nil {
			return err
		}
		return conn.Close()
	case "dns":
		_, err := net.DefaultResolver.LookupHost(ctx, e.Target)
		return err
	default:
		return fmt.Errorf("unknown endpoint kind %q", e.Kind)
	}
}
