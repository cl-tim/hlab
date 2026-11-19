package inventory

import (
	"fmt"

	"github.com/cl-tim/hlab/internal/selector"
)

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
//
// The expression language (all, @group, #tag, name/glob, !exclusion) lives in
// internal/selector, shared with the printer inventory.
func (i *Inventory) SelectAll(expr string) ([]Host, error) {
	return selector.Set[Host]{
		Noun:   "host",
		Items:  i.hosts,
		Groups: i.groups,
		Name:   func(h Host) string { return h.Name },
		HasTag: Host.HasTag,
	}.Select(expr)
}
