// Package selector resolves target expressions against a named, tagged,
// grouped set of things. Hosts and printers are separate inventories with
// separate rules about what may be connected to, but the expression language
// is one language, and this is the only implementation of it.
package selector

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

// Set describes what an expression is resolved against.
type Set[T any] struct {
	// Noun names one item in error messages: "host", "printer".
	Noun string
	// Items is every candidate.
	Items []T
	// Groups maps a group name to member names. Members are assumed to have
	// been validated against Items when the inventory was built.
	Groups map[string][]string

	Name   func(T) string
	HasTag func(T, string) bool
}

// Select resolves an expression into a de-duplicated, name-sorted list.
//
// The expression is a comma separated list of terms. Each term is one of:
//
//	all          every item
//	@group       every member of the named group
//	#tag         every item carrying the tag
//	name         an item name, which may contain glob wildcards (web-*)
//
// A term prefixed with '!' subtracts its matches from the set instead of
// adding them, so "@prod,!#retired" means "everything in prod that is not
// tagged retired". Exclusions are applied after all inclusions regardless of
// where they appear in the expression.
func (s Set[T]) Select(expr string) ([]T, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return nil, fmt.Errorf("empty %s selector", s.Noun)
	}

	byName := make(map[string]T, len(s.Items))
	for _, item := range s.Items {
		byName[s.Name(item)] = item
	}

	include := map[string]T{}
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

		matches, err := s.matchTerm(term, byName)
		if err != nil {
			return nil, err
		}

		for _, item := range matches {
			if negate {
				exclude[s.Name(item)] = struct{}{}
			} else {
				include[s.Name(item)] = item
			}
		}
	}

	out := make([]T, 0, len(include))
	for name, item := range include {
		if _, skip := exclude[name]; skip {
			continue
		}
		out = append(out, item)
	}
	sort.Slice(out, func(a, b int) bool { return s.Name(out[a]) < s.Name(out[b]) })

	if len(out) == 0 {
		return nil, fmt.Errorf("selector %q matched no %ss", expr, s.Noun)
	}
	return out, nil
}

// matchTerm resolves a single selector term. An unmatched term is an error
// rather than an empty result, so a typo fails loudly instead of quietly
// running a command on nothing.
func (s Set[T]) matchTerm(term string, byName map[string]T) ([]T, error) {
	switch {
	case term == "all":
		return append([]T(nil), s.Items...), nil

	case strings.HasPrefix(term, "@"):
		name := strings.TrimPrefix(term, "@")
		members, ok := s.Groups[name]
		if !ok {
			return nil, fmt.Errorf("unknown group %q%s", name, s.knownGroups())
		}
		items := make([]T, 0, len(members))
		for _, m := range members {
			if item, ok := byName[m]; ok {
				items = append(items, item)
			}
		}
		return items, nil

	case strings.HasPrefix(term, "#"):
		tag := strings.TrimPrefix(term, "#")
		var items []T
		for _, item := range s.Items {
			if s.HasTag(item, tag) {
				items = append(items, item)
			}
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("no %ss carry tag %q", s.Noun, tag)
		}
		return items, nil

	default:
		if item, ok := byName[term]; ok {
			return []T{item}, nil
		}
		// Fall back to glob matching on the name.
		var items []T
		for _, item := range s.Items {
			if ok, err := path.Match(term, s.Name(item)); err == nil && ok {
				items = append(items, item)
			}
		}
		if len(items) == 0 {
			return nil, fmt.Errorf("unknown %s %q", s.Noun, term)
		}
		return items, nil
	}
}

func (s Set[T]) knownGroups() string {
	if len(s.Groups) == 0 {
		return ""
	}
	names := make([]string, 0, len(s.Groups))
	for name := range s.Groups {
		names = append(names, name)
	}
	sort.Strings(names)
	return " (known groups: " + strings.Join(names, ", ") + ")"
}
