package inventory

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Select resolves a target expression into a concrete, de-duplicated,
// name-sorted list of hosts.
//
// The expression is a comma separated list of terms. Each term is one of:
//
//	all          every host in the inventory
//	@group       every member of the named group
//	#tag         every host carrying the tag
//	name         a host name, which may contain glob wildcards (web-*)
//
// A term prefixed with '!' subtracts its matches from the set instead of
// adding them, so "@prod,!#retired" means "everything in prod that is not
// tagged retired". Exclusions are applied after all inclusions regardless of
// where they appear in the expression.
// Select resolves a target expression to machines that can actually be reached,
// i.e. managed ones. Commands that connect must use this rather than SelectAll:
// an unmanaged machine has no credentials, so targeting it could only fail, and
// silently including every device on the network in `all` would be dangerous.
//
// Matching unmanaged machines are reported in skipped so callers can say so.
func (i *Inventory) Select(expr string) (selected, skipped []Host, err error) {
	matched, err := i.SelectAll(expr)
	if err != nil {
		return nil, nil, err
	}

	for _, h := range matched {
		if h.Managed {
			selected = append(selected, h)
		} else {
			skipped = append(skipped, h)
		}
	}

	if len(selected) == 0 {
		return nil, nil, fmt.Errorf(
			"selector %q matched %d machine(s), none of them managed by hlab.\n"+
				"Bring them under management first: hlab discover scan --adopt", expr, len(matched))
	}
	return selected, skipped, nil
}

// SelectAll resolves a target expression against every known machine, managed
// or not. It backs listing and reporting, never connecting.
func (i *Inventory) SelectAll(expr string) ([]Host, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty target selector")
	}

	include := map[string]Host{}
	exclude := map[string]struct{}{}

	for _, raw := range strings.Split(expr, ",") {
		term := strings.TrimSpace(raw)
		if term == "" {
			continue
		}

		negate := false
		if strings.HasPrefix(term, "!") {
			negate = true
			term = strings.TrimSpace(strings.TrimPrefix(term, "!"))
			if term == "" {
				return nil, fmt.Errorf("selector term %q has nothing to exclude", raw)
			}
		}

		matches, err := i.matchTerm(term)
		if err != nil {
			return nil, err
		}

		for _, h := range matches {
			if negate {
				exclude[h.Name] = struct{}{}
			} else {
				include[h.Name] = h
			}
		}
	}

	out := make([]Host, 0, len(include))
	for name, h := range include {
		if _, skip := exclude[name]; skip {
			continue
		}
		out = append(out, h)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })

	if len(out) == 0 {
		return nil, fmt.Errorf("selector %q matched no hosts", expr)
	}
	return out, nil
}

// matchTerm resolves a single selector term. An unmatched term is an error
// rather than an empty result, so a typo fails loudly instead of quietly
// running a command on nothing.
func (i *Inventory) matchTerm(term string) ([]Host, error) {
	switch {
	case term == "all":
		return i.Hosts(), nil

	case strings.HasPrefix(term, "@"):
		name := strings.TrimPrefix(term, "@")
		members, ok := i.groups[name]
		if !ok {
			return nil, fmt.Errorf("unknown group %q (known groups: %s)", name, strings.Join(i.GroupNames(), ", "))
		}
		hosts := make([]Host, 0, len(members))
		for _, m := range members {
			hosts = append(hosts, i.byName[m])
		}
		return hosts, nil

	case strings.HasPrefix(term, "#"):
		tag := strings.TrimPrefix(term, "#")
		var hosts []Host
		for _, h := range i.hosts {
			if h.HasTag(tag) {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			return nil, fmt.Errorf("no hosts carry tag %q", tag)
		}
		return hosts, nil

	default:
		if h, ok := i.byName[term]; ok {
			return []Host{h}, nil
		}
		// Fall back to glob matching on the host name.
		var hosts []Host
		for _, h := range i.hosts {
			if ok, err := path.Match(term, h.Name); err == nil && ok {
				hosts = append(hosts, h)
			}
		}
		if len(hosts) == 0 {
			return nil, fmt.Errorf("unknown host %q", term)
		}
		return hosts, nil
	}
}
