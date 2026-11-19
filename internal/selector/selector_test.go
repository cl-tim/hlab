package selector

import (
	"reflect"
	"strings"
	"testing"
)

type thing struct {
	name string
	tags []string
}

func set() Set[thing] {
	return Set[thing]{
		Noun: "thing",
		Items: []thing{
			{name: "db-01", tags: []string{"prod"}},
			{name: "web-01", tags: []string{"prod"}},
			{name: "web-02", tags: []string{"Retired"}},
		},
		Groups: map[string][]string{"web": {"web-02", "web-01"}},
		Name:   func(t thing) string { return t.name },
		HasTag: func(t thing, tag string) bool {
			for _, candidate := range t.tags {
				if strings.EqualFold(candidate, tag) {
					return true
				}
			}
			return false
		},
	}
}

func names(items []thing) []string {
	out := make([]string, len(items))
	for i, item := range items {
		out[i] = item.name
	}
	return out
}

func TestSelect(t *testing.T) {
	cases := map[string][]string{
		"all":              {"db-01", "web-01", "web-02"},
		"@web":             {"web-01", "web-02"},
		"#prod":            {"db-01", "web-01"},
		"web-*":            {"web-01", "web-02"},
		"db-01, web-01":    {"db-01", "web-01"},
		"!#retired, @web":  {"web-01"},
		"all,!web-*":       {"db-01"},
		"web-01,web-01":    {"web-01"},
		" all , , !db-01 ": {"web-01", "web-02"},
	}
	for expr, want := range cases {
		got, err := set().Select(expr)
		if err != nil {
			t.Errorf("Select(%q): %v", expr, err)
			continue
		}
		if !reflect.DeepEqual(names(got), want) {
			t.Errorf("Select(%q) = %v, want %v", expr, names(got), want)
		}
	}
}

// A typo must fail loudly rather than run a command on nothing.
func TestSelectErrorsNameTheNoun(t *testing.T) {
	cases := map[string]string{
		"":             "empty thing selector",
		"nope":         `unknown thing "nope"`,
		"@nope":        `unknown group "nope" (known groups: web)`,
		"#nope":        `no things carry tag "nope"`,
		"!":            "nothing to exclude",
		"web-01,!@web": "matched no things",
	}
	for expr, want := range cases {
		_, err := set().Select(expr)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("Select(%q) error = %v, want it to contain %q", expr, err, want)
		}
	}
}

func TestSelectAllDoesNotAliasItems(t *testing.T) {
	s := set()
	got, err := s.Select("all")
	if err != nil {
		t.Fatal(err)
	}
	got[0].name = "changed"
	if s.Items[0].name != "db-01" {
		t.Error("Select returned a slice sharing storage with Items")
	}
}
