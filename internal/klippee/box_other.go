//go:build !linux

package klippee

import (
	"context"
	"fmt"
)

func (c *Client) queryBoxReset(context.Context) (map[string]any, error) {
	return nil, fmt.Errorf("Box reset-reason query requires a Linux printer host")
}
