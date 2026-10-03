package config

import (
	"errors"
	"fmt"
	"os"
)

// MoveHost repoints a record at the address its machine now answers on.
//
// A scan that ran after the move will have recorded the new address as an
// unmanaged machine of its own. The caller has proved the machine there is
// this one, so that record is a duplicate and is absorbed; its name is
// returned so the caller can say so. A *managed* record at the new address is
// a genuine conflict — two credentials claiming one endpoint — and is refused
// rather than resolved by guessing.
func MoveHost(path, name, address string) (absorbed string, err error) {
	if path == "" {
		return "", errors.New("no config path given")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	file, err := parseBytes(raw)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", path, err)
	}
	hosts := migrateLegacyDiscovered(file)

	at := -1
	for i, h := range hosts {
		if h.Name == name {
			at = i
		}
	}
	if at < 0 {
		return "", fmt.Errorf("no machine named %q", name)
	}
	port := hosts[at].Port

	kept := hosts[:0]
	for i, h := range hosts {
		if i != at && h.Address == address && samePort(h.Port, port) {
			if h.Managed {
				return "", fmt.Errorf(
					"%s is recorded for managed machine %q too; resolve that by hand before moving %q there",
					address, h.Name, name)
			}
			absorbed = h.Name
			continue
		}
		if i == at {
			h.Address = address
		}
		kept = append(kept, h)
	}

	updated, err := replaceHostsSection(raw, kept)
	if err != nil {
		return "", err
	}
	if absorbed != "" {
		if updated, err = dropFromGroups(updated, map[string]struct{}{absorbed: {}}); err != nil {
			return "", err
		}
	}

	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return "", fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	return absorbed, writeAtomic(path, updated)
}

// samePort compares ports where zero means the SSH default.
func samePort(a, b int) bool {
	if a == 0 {
		a = 22
	}
	if b == 0 {
		b = 22
	}
	return a == b
}
