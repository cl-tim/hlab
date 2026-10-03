package config

import (
	"errors"
	"fmt"
	"os"
	"sort"
)

// UnmanageHost turns a managed record back into an observed one: the machine
// stays in the inventory as a known device, but hlab no longer holds a way in.
//
// The user and identity file go with it. They described access that has just
// been revoked, and leaving them would make the record claim a credential that
// no longer works — and make re-enrolling look like it had nothing to do.
// Everything observed about the machine is kept.
//
// Group memberships are dropped, returning the groups it was removed from: a
// group lists machines to act on, and an unmanaged member could only ever be
// reported as skipped.
func UnmanageHost(path, name string) ([]string, error) {
	if path == "" {
		return nil, errors.New("no config path given")
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	file, err := parseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	hosts := migrateLegacyDiscovered(file)
	found := false
	for i := range hosts {
		if hosts[i].Name != name {
			continue
		}
		hosts[i].Managed = false
		hosts[i].User = ""
		hosts[i].IdentityFile = ""
		found = true
	}
	if !found {
		return nil, fmt.Errorf("no machine named %q", name)
	}

	var groups []string
	for g, members := range file.Groups {
		for _, m := range members {
			if m == name {
				groups = append(groups, g)
				break
			}
		}
	}

	sort.Strings(groups)

	updated, err := replaceHostsSection(raw, hosts)
	if err != nil {
		return nil, err
	}
	if updated, err = dropFromGroups(updated, map[string]struct{}{name: {}}); err != nil {
		return nil, err
	}

	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return nil, fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	return groups, writeAtomic(path, updated)
}
