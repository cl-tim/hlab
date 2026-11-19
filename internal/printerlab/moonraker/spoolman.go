package moonraker

import (
	"context"
	"fmt"
)

type SpoolmanStatus struct {
	Connected      bool         `json:"connected" yaml:"connected"`
	ActiveSpoolID  *int         `json:"activeSpoolId" yaml:"activeSpoolId"`
	PendingReports []SpoolUsage `json:"pendingReports" yaml:"pendingReports"`
}

type SpoolUsage struct {
	SpoolID        int     `json:"spoolId" yaml:"spoolId"`
	FilamentUsedMM float64 `json:"filamentUsedMm" yaml:"filamentUsedMm"`
}

func (c *Client) SpoolmanStatus(ctx context.Context) (SpoolmanStatus, error) {
	var envelope struct {
		Result struct {
			Connected *bool `json:"spoolman_connected"`
			SpoolID   *int  `json:"spool_id"`
			Pending   []struct {
				ID       int     `json:"spool_id"`
				Filament float64 `json:"filament_used"`
			} `json:"pending_reports"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/server/spoolman/status", &envelope); err != nil {
		return SpoolmanStatus{}, err
	}
	result := envelope.Result
	if result.Connected == nil {
		return SpoolmanStatus{}, fmt.Errorf("Moonraker Spoolman response omitted connection status")
	}
	status := SpoolmanStatus{
		Connected: *result.Connected, ActiveSpoolID: result.SpoolID,
		PendingReports: make([]SpoolUsage, 0, len(result.Pending)),
	}
	for _, pending := range result.Pending {
		status.PendingReports = append(status.PendingReports, SpoolUsage{pending.ID, pending.Filament})
	}
	return status, nil
}
