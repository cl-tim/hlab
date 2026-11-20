package klippee

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/user"
	"strings"
)

func (c *Client) setVariableProof(ctx context.Context, name string) error {
	if c.Config.ClientMode != "printer" || len(name) > 64 || !variableName.MatchString(name) {
		return fmt.Errorf("expected a valid printer variable name")
	}
	account, err := user.Current()
	if err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	value, err := io.ReadAll(io.LimitReader(c.In, 4097))
	if err != nil {
		return err
	}
	if len(value) > 4096 || strings.ContainsAny(string(value), "\x00\r\n") {
		return fmt.Errorf("variable value must be a single line of at most 4096 bytes")
	}
	proof := map[string]any{
		"schema": "qidi-plus4-klippee-variable-set/v1", "account": account.Username, "cli_path": executable,
		"name": name, "value_sha256": fmt.Sprintf("%x", sha256.Sum256(value)),
		"ok": false, "verified": false, "set_return_code": -1, "doctor_return_code": nil,
		"config_deployed": false, "klipper_action": "none", "error": "",
	}
	verify := func() error {
		unlock, err := c.lock()
		if err != nil {
			return err
		}
		defer unlock()
		if err := c.requireIdle(ctx); err != nil {
			return err
		}
		if err := c.variableCommand([]string{"set", name, string(value)}); err != nil {
			proof["set_return_code"] = 1
			return err
		}
		proof["set_return_code"] = 0
		checker := *c
		checker.Out = io.Discard
		if err := checker.variableCommand([]string{"doctor"}); err != nil {
			proof["doctor_return_code"] = 1
			return err
		}
		proof["doctor_return_code"] = 0
		lines, err := c.variableLines()
		if err != nil {
			return err
		}
		for _, line := range lines {
			key, stored, ok := variableEntry(line)
			if ok && key == name && stored == string(value) {
				return nil
			}
		}
		return fmt.Errorf("could not verify stored variable")
	}
	resultErr := verify()
	if resultErr == nil {
		proof["ok"], proof["verified"] = true, true
	} else {
		proof["error"] = resultErr.Error()
	}
	if err := json.NewEncoder(c.output()).Encode(proof); err != nil {
		return err
	}
	return resultErr
}
