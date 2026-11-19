package moonraker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

type Job struct {
	ID                   string    `json:"id" yaml:"id"`
	Filename             string    `json:"filename" yaml:"filename"`
	Status               string    `json:"status" yaml:"status"`
	StartedAt            time.Time `json:"startedAt" yaml:"startedAt"`
	PrintDurationSeconds float64   `json:"printDurationSeconds" yaml:"printDurationSeconds"`
	FilamentUsedMM       float64   `json:"filamentUsedMm" yaml:"filamentUsedMm"`
	SpoolIDs             []int     `json:"spoolIds,omitempty" yaml:"spoolIds,omitempty"`
}

// JobHistory returns the most recent jobs, optionally filtered by start time.
func (c *Client) JobHistory(ctx context.Context, since time.Time, limit int) ([]Job, error) {
	query := url.Values{"order": {"desc"}, "limit": {strconv.Itoa(limit)}}
	if !since.IsZero() {
		query.Set("since", strconv.FormatFloat(float64(since.Unix())+float64(since.Nanosecond())/1e9, 'f', -1, 64))
	}
	var envelope struct {
		Result struct {
			Jobs []struct {
				ID        string  `json:"job_id"`
				Filename  string  `json:"filename"`
				Status    string  `json:"status"`
				StartTime float64 `json:"start_time"`
				PrintTime float64 `json:"print_duration"`
				Filament  float64 `json:"filament_used"`
				Auxiliary []struct {
					Provider string          `json:"provider"`
					Name     string          `json:"name"`
					Value    json.RawMessage `json:"value"`
				} `json:"auxiliary_data"`
			} `json:"jobs"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/server/history/list?"+query.Encode(), &envelope); err != nil {
		return nil, err
	}
	if envelope.Result.Jobs == nil {
		return nil, fmt.Errorf("Moonraker history response omitted jobs")
	}
	jobs := make([]Job, 0, len(envelope.Result.Jobs))
	for _, raw := range envelope.Result.Jobs {
		seconds := int64(raw.StartTime)
		job := Job{
			ID: raw.ID, Filename: raw.Filename, Status: raw.Status,
			StartedAt:            time.Unix(seconds, int64((raw.StartTime-float64(seconds))*1e9)).UTC(),
			PrintDurationSeconds: raw.PrintTime, FilamentUsedMM: raw.Filament,
		}
		for _, field := range raw.Auxiliary {
			if field.Provider == "spoolman" && field.Name == "spool_ids" {
				if err := json.Unmarshal(field.Value, &job.SpoolIDs); err != nil {
					return nil, fmt.Errorf("decode Spoolman spool IDs for job %s: %w", raw.ID, err)
				}
			}
		}
		jobs = append(jobs, job)
	}
	return jobs, nil
}

func (c *Client) TotalPrintSeconds(ctx context.Context) (float64, error) {
	var envelope struct {
		Result struct {
			Totals struct {
				PrintTime *float64 `json:"total_print_time"`
			} `json:"job_totals"`
		} `json:"result"`
	}
	if err := c.get(ctx, "/server/history/totals", &envelope); err != nil {
		return 0, err
	}
	seconds := envelope.Result.Totals.PrintTime
	if seconds == nil || *seconds < 0 {
		return 0, fmt.Errorf("Moonraker history response omitted a valid print total")
	}
	return *seconds, nil
}
