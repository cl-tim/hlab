package klippee

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const moonrakerFrameLimit = 8 << 20

type moonrakerMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	Result json.RawMessage `json:"result"`
	Error  json.RawMessage `json:"error"`
}

type moonrakerSocket struct {
	net.Conn
	reader *bufio.Scanner
}

func openMoonraker(ctx context.Context, path string) (*moonrakerSocket, error) {
	conn, err := (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, "unix", path)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(conn)
	scanner.Buffer(make([]byte, 4096), moonrakerFrameLimit)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if end := bytes.IndexByte(data, 3); end >= 0 {
			return end + 1, data[:end], nil
		}
		if atEOF && len(data) > 0 {
			return 0, nil, io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	})
	return &moonrakerSocket{Conn: conn, reader: scanner}, nil
}

func (s *moonrakerSocket) receive() (moonrakerMessage, error) {
	for s.reader.Scan() {
		var msg moonrakerMessage
		if json.Unmarshal(s.reader.Bytes(), &msg) == nil {
			return msg, nil
		}
	}
	if err := s.reader.Err(); err != nil {
		return moonrakerMessage{}, err
	}
	return moonrakerMessage{}, io.EOF
}

func (s *moonrakerSocket) send(msg map[string]any) error {
	msg["jsonrpc"] = "2.0"
	payload, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	_, err = s.Write(append(payload, 3))
	return err
}

func (s *moonrakerSocket) call(id string, method string, params any) (json.RawMessage, error) {
	if err := s.send(map[string]any{"id": id, "method": method, "params": params}); err != nil {
		return nil, err
	}
	expected, _ := json.Marshal(id)
	for {
		msg, err := s.receive()
		if err != nil {
			return nil, err
		}
		if !bytes.Equal(msg.ID, expected) || msg.Method != "" {
			continue
		}
		if len(msg.Result) == 0 || len(msg.Error) != 0 {
			return nil, fmt.Errorf("Moonraker rejected %s", method)
		}
		return msg.Result, nil
	}
}

func (c *Client) moonrakerPath() string {
	if path := os.Getenv("KLIPPEE_MOONRAKER_SOCKET"); path != "" {
		return path
	}
	return filepath.Join(filepath.Dir(c.Config.ConfigDir), "comms", "moonraker.sock")
}

func (c *Client) moonrakerCall(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	socket, err := openMoonraker(ctx, c.moonrakerPath())
	if err != nil {
		return nil, err
	}
	defer socket.Close()
	stop := context.AfterFunc(ctx, func() { _ = socket.Close() })
	defer stop()
	return socket.call("hlab", method, params)
}

func (c *Client) requireIdle(ctx context.Context) error {
	result, err := c.moonrakerCall(ctx, "printer.objects.query", map[string]any{
		"objects": map[string]any{"print_stats": []string{"state"}},
	})
	if err != nil {
		return fmt.Errorf("could not confirm fresh printer state: %w", err)
	}
	var payload struct {
		Status struct {
			PrintStats struct{ State string } `json:"print_stats"`
		}
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return fmt.Errorf("invalid printer state: %w", err)
	}
	switch state := strings.ToLower(strings.TrimSpace(payload.Status.PrintStats.State)); state {
	case "standby", "complete", "error", "cancelled":
		return nil
	default:
		return fmt.Errorf("operation blocked while fresh printer state is %q", state)
	}
}
