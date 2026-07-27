package config

import (
	"errors"
	"fmt"
	"net/netip"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// validHostName is deliberately narrow: a record name is used in selectors, so
// it must not contain the characters those treat specially (@ # , ! * and
// whitespace).
var validHostName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,62}$`)

// ValidateHostName rejects names that a selector could not address.
func ValidateHostName(name string) error {
	if !validHostName.MatchString(name) {
		return fmt.Errorf(
			"invalid name %q: use letters, digits, dot, dash and underscore, starting with a letter or digit", name)
	}
	// "all" is the wildcard selector, so a machine may not claim it.
	if name == "all" {
		return errors.New(`"all" is reserved: it is the selector meaning every machine`)
	}
	return nil
}

// RenameHost changes a machine's recorded name, leaving everything else about
// the record — and the rest of the file — untouched.
//
// Group membership is updated alongside it, so a rename cannot leave a group
// pointing at a machine that no longer exists.
func RenameHost(path, oldName, newName string) error {
	if path == "" {
		return errors.New("no config path given")
	}
	if err := ValidateHostName(newName); err != nil {
		return err
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}

	file, err := parseBytes(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	hosts := migrateLegacyDiscovered(file)

	found := false
	for i := range hosts {
		switch hosts[i].Name {
		case newName:
			return fmt.Errorf("%q is already used by another machine", newName)
		case oldName:
			hosts[i].Name = newName
			found = true
		}
	}
	if !found {
		return fmt.Errorf("no machine named %q", oldName)
	}

	updated, err := replaceHostsSection(raw, hosts)
	if err != nil {
		return err
	}
	if updated, err = renameInGroups(updated, oldName, newName); err != nil {
		return err
	}

	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	return writeAtomic(path, updated)
}

// renameInGroups repoints any group membership at the new name.
func renameInGroups(raw []byte, oldName, newName string) ([]byte, error) {
	file, err := parseBytes(raw)
	if err != nil {
		return nil, err
	}
	if len(file.Groups) == 0 {
		return raw, nil
	}

	changed := false
	groups := make(map[string][]string, len(file.Groups))
	for name, members := range file.Groups {
		updated := make([]string, 0, len(members))
		for _, m := range members {
			if m == oldName {
				m = newName
				changed = true
			}
			updated = append(updated, m)
		}
		groups[name] = updated
	}
	if !changed {
		return raw, nil
	}

	return rewriteGroups(raw, groups)
}

// rewriteGroups replaces the membership lists of the groups given, leaving
// group names, their order, and everything else in the document alone.
func rewriteGroups(raw []byte, groups map[string][]string) ([]byte, error) {
	doc, root, err := documentRoot(raw)
	if err != nil {
		return nil, err
	}

	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value != "groups" {
			continue
		}
		mapping := root.Content[i+1]
		if mapping.Kind != yaml.MappingNode {
			break
		}
		for j := 0; j+1 < len(mapping.Content); j += 2 {
			members, ok := groups[mapping.Content[j].Value]
			if !ok {
				continue
			}
			var node yaml.Node
			if err := node.Encode(members); err != nil {
				return nil, fmt.Errorf("render group %s: %w", mapping.Content[j].Value, err)
			}
			mapping.Content[j+1] = &node
		}
		break
	}

	out, err := marshalDocument(doc)
	if err != nil {
		return nil, err
	}
	if _, err := parseBytes(out); err != nil {
		return nil, fmt.Errorf("refusing to write a config that would not load back: %w", err)
	}
	return out, nil
}

// SetHostAddress changes the address a machine is reached at, leaving the rest
// of the record alone.
func SetHostAddress(path, name, address string) error {
	if path == "" {
		return errors.New("no config path given")
	}
	if _, err := netip.ParseAddr(address); err != nil {
		return fmt.Errorf("invalid address %q: %w", address, err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	file, err := parseBytes(raw)
	if err != nil {
		return fmt.Errorf("parse %s: %w", path, err)
	}
	hosts := migrateLegacyDiscovered(file)

	found := false
	for i := range hosts {
		if hosts[i].Name == name {
			hosts[i].Address = address
			found = true
		} else if hosts[i].Address == address {
			return fmt.Errorf("%s is already used by %s", address, hosts[i].Name)
		}
	}
	if !found {
		return fmt.Errorf("no machine named %q", name)
	}

	updated, err := replaceHostsSection(raw, hosts)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".bak", raw, 0o600); err != nil {
		return fmt.Errorf("write backup %s.bak: %w", path, err)
	}
	return writeAtomic(path, updated)
}
