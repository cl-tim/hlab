package klippee

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cl-tim/hlab/internal/buildinfo"
)

type agentAction struct {
	command string
	mutates bool
	run     func(context.Context, *Client) error
}

var agentActions = map[string]agentAction{
	"klippee_apply":   {"apply", true, func(ctx context.Context, c *Client) error { return c.Apply(ctx, true, false, "") }},
	"klippee_pull":    {"apply", true, func(ctx context.Context, c *Client) error { return c.Apply(ctx, false, true, "") }},
	"klippee_capture": {"capture", true, captureFromDashboard},
	"klippee_push":    {"capture", true, captureFromDashboard},
	"klippee_check":   {"check", false, checkFromDashboard},
	"klippee_status":  {"check", false, checkFromDashboard},
	"klippee_upload_diagnostics": {"upload-diagnostics", false, func(ctx context.Context, c *Client) error {
		return c.UploadDiagnostics(ctx, "", false)
	}},
	"klippee_query_box_reset_reason": {"query-box-reset-reason", false, writeBoxReset},
}

func captureFromDashboard(ctx context.Context, c *Client) error {
	return c.Capture(ctx, "Captured from Mainsail/Fluidd")
}

func checkFromDashboard(ctx context.Context, c *Client) error { return c.Check(ctx, false) }

type agentOperation struct {
	ID         string          `json:"id"`
	Method     string          `json:"method"`
	Command    string          `json:"command"`
	Status     string          `json:"status"`
	StartedAt  string          `json:"started_at"`
	FinishedAt string          `json:"completed_at,omitempty"`
	ReturnCode *int            `json:"return_code,omitempty"`
	Summary    string          `json:"summary,omitempty"`
	Result     json.RawMessage `json:"result,omitempty"`
}

type moonrakerAgent struct {
	prototype *Client
	load      func(context.Context) (Config, error)
	mu        sync.Mutex
	active    *agentOperation
	last      *agentOperation
}

func (a *moonrakerAgent) status(params json.RawMessage) (any, error) {
	var query map[string]json.RawMessage
	if len(params) > 0 && json.Unmarshal(params, &query) != nil {
		return nil, fmt.Errorf("operation status expects an object")
	}
	var id string
	if raw, ok := query["operation_id"]; ok {
		if json.Unmarshal(raw, &id) != nil || strings.TrimSpace(id) == "" || len(id) > 128 {
			return nil, fmt.Errorf("invalid operation_id")
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	current, last := a.active, a.last
	if id != "" {
		if current != nil && current.ID != id {
			current = nil
		}
		if last != nil && last.ID != id {
			last = nil
		}
	} else if current != nil {
		id = current.ID
	} else if last != nil {
		id = last.ID
	}
	return map[string]any{
		"running": current != nil, "found": current != nil || last != nil,
		"operation_id": id, "current": current, "last_result": last,
	}, nil
}

func (a *moonrakerAgent) receive(ctx context.Context, msg moonrakerMessage) (any, error) {
	if msg.Method == "klippee_operation_status" {
		return a.status(msg.Params)
	}
	action, ok := agentActions[msg.Method]
	if !ok {
		return nil, fmt.Errorf("unknown Klippee agent method")
	}
	a.mu.Lock()
	busy := a.active != nil
	a.mu.Unlock()
	if busy {
		return nil, fmt.Errorf("another Klippee operation is running")
	}
	client := *a.prototype
	config, err := a.load(ctx)
	if err != nil {
		return nil, err
	}
	if config.ClientMode != "printer" || !samePath(config.ConfigDir, client.Config.ConfigDir) {
		return nil, fmt.Errorf("printer connection changed; restart the Klippee agent")
	}
	client.Config = config
	if action.mutates {
		if err := client.requireIdle(ctx); err != nil {
			return nil, err
		}
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return nil, err
	}
	op := agentOperation{ID: hex.EncodeToString(random[:]), Method: msg.Method, Command: action.command,
		Status: "running", StartedAt: time.Now().UTC().Format(time.RFC3339)}
	a.mu.Lock()
	a.active = &op
	a.mu.Unlock()
	go a.execute(ctx, op, &client, action)
	return map[string]any{"accepted": true, "command": action.command, "operation_id": op.ID}, nil
}

type agentOutput struct{ bytes.Buffer }

func (b *agentOutput) Write(data []byte) (int, error) {
	const limit = 8192
	if len(data) >= limit {
		b.Reset()
		_, _ = b.Buffer.Write(data[len(data)-limit:])
		return len(data), nil
	}
	if overflow := b.Len() + len(data) - limit; overflow > 0 {
		b.Next(overflow)
	}
	return b.Buffer.Write(data)
}

func (a *moonrakerAgent) execute(ctx context.Context, op agentOperation, client *Client, action agentAction) {
	ctx, cancel := context.WithTimeout(ctx, 180*time.Second)
	defer cancel()
	var output agentOutput
	client.Out, client.ErrOut = &output, &output
	err := action.run(ctx, client)
	code := 0
	op.Status = "succeeded"
	if err != nil {
		fmt.Fprintln(&output, err)
		op.Status, code = "failed", 1
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			op.Status = "timed_out"
		}
	}
	summary := strings.Join(strings.Fields(output.String()), " ")
	if err == nil && op.Command == "query-box-reset-reason" {
		op.Result = bytes.TrimSpace(bytes.Clone(output.Bytes()))
		summary = "Read QIDI Box reset reason and flags"
	}
	if client.Config.Token != "" {
		summary = strings.ReplaceAll(summary, client.Config.Token, "[redacted]")
	}
	if len(summary) > 2000 {
		summary = summary[len(summary)-2000:]
	}
	op.Summary, op.ReturnCode, op.FinishedAt = summary, &code, time.Now().UTC().Format(time.RFC3339)
	a.mu.Lock()
	a.active, a.last = nil, &op
	a.mu.Unlock()
	if op.Command != "query-box-reset-reason" && op.Command != "upload-diagnostics" {
		message := fmt.Sprintf("RESPOND TYPE=echo MSG=\"Klippee %s %s\"", op.Command, op.Status)
		_, _ = client.moonrakerCall(ctx, "printer.gcode.script", map[string]string{"script": message})
	}
}

func (a *moonrakerAgent) connected(ctx context.Context) error {
	socket, err := openMoonraker(ctx, a.prototype.moonrakerPath())
	if err != nil {
		return err
	}
	defer socket.Close()
	stop := context.AfterFunc(ctx, func() { _ = socket.Close() })
	defer stop()
	_ = socket.SetDeadline(time.Now().Add(10 * time.Second))
	_, err = socket.call("identify", "server.connection.identify", map[string]string{
		"client_name": "klippee", "type": "agent", "version": buildinfo.Version, "url": "https://klippee.xyz",
	})
	if err != nil {
		return err
	}
	methods := make([]string, 0, len(agentActions))
	for method := range agentActions {
		methods = append(methods, method)
	}
	sort.Strings(methods)
	for index, method := range methods {
		_ = socket.SetDeadline(time.Now().Add(5 * time.Second))
		if _, err := socket.call(strconv.Itoa(index), "connection.register_remote_method", map[string]string{"method_name": method}); err != nil {
			return err
		}
	}
	_ = socket.SetDeadline(time.Time{})
	fmt.Fprintln(a.prototype.output(), "Klippee agent connected to Moonraker")
	for {
		msg, err := socket.receive()
		if err != nil {
			return err
		}
		if msg.Method == "" || strings.HasPrefix(msg.Method, "notify_") {
			continue
		}
		result, actionErr := a.receive(ctx, msg)
		if len(msg.ID) == 0 || string(msg.ID) == "null" {
			if actionErr != nil {
				fmt.Fprintf(a.prototype.output(), "Klippee agent refused %s: %v\n", msg.Method, actionErr)
			}
			continue
		}
		response := map[string]any{"id": msg.ID, "result": result}
		if actionErr != nil {
			delete(response, "result")
			response["error"] = map[string]any{"code": -32000, "message": actionErr.Error()}
		}
		_ = socket.SetWriteDeadline(time.Now().Add(5 * time.Second))
		if err := socket.send(response); err != nil {
			return err
		}
	}
}

func (c *Client) agent(ctx context.Context, connection string) error {
	if c.Config.ClientMode != "printer" {
		return fmt.Errorf("Moonraker agent requires a printer connection")
	}
	a := moonrakerAgent{prototype: c, load: func(ctx context.Context) (Config, error) {
		return Load(ctx, connection, "")
	}}
	for ctx.Err() == nil {
		if err := a.connected(ctx); err != nil && ctx.Err() == nil {
			fmt.Fprintf(c.output(), "Klippee agent disconnected: %v\n", err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(5 * time.Second):
		}
	}
	return nil
}
