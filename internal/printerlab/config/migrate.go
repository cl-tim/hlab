package config

import (
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Init adds a starter printer inventory to path, creating the file if it does
// not exist. In hlab's config it becomes the `printerlab:` section; nothing
// else in the file is changed. A file that already holds a printer inventory,
// in either layout, is refused rather than overwritten.
func Init(path, starter string) error {
	_, inventory, err := parseRoot([]byte(starter))
	if err != nil {
		return fmt.Errorf("starter inventory: %w", err)
	}
	return editRoot(path, "printer-init", func(root *yaml.Node) error {
		if err := requireNoInventory(root, path); err != nil {
			return err
		}
		attach(root, inventory, "Printers, managed with `hlab printer`.")
		return nil
	})
}

// Migrate folds the standalone printer inventory in from into hlab's config
// at to, as its `printerlab:` section.
//
// The source's YAML nodes are moved rather than re-marshalled from structs,
// so the operator's comments and key order arrive intact. Nothing is merged:
// a target that already has a printer section is refused, because choosing
// between two inventories is not something to do silently. The source file is
// left exactly as it was; retiring it is the caller's decision.
func Migrate(from, to string) error {
	source, err := loadFile(from)
	if err != nil {
		return err
	}
	if source.Layout != LayoutStandalone {
		return fmt.Errorf("%s is not a standalone printer inventory", from)
	}
	if same, err := samePath(from, to); err != nil {
		return err
	} else if same {
		return fmt.Errorf("%s is both the source and the target", from)
	}
	raw, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	doc, inventory, err := parseRoot(raw)
	if err != nil {
		return err
	}
	comment := doc.HeadComment
	if comment == "" {
		comment = "Printers, managed with `hlab printer`."
	}
	return editRoot(to, "printer-migrate", func(root *yaml.Node) error {
		if err := requireNoInventory(root, to); err != nil {
			return err
		}
		attach(root, inventory, comment)
		return nil
	})
}

func requireNoInventory(root *yaml.Node, path string) error {
	layout, err := layoutOf(root)
	if err != nil {
		return err
	}
	if layout == LayoutStandalone {
		return fmt.Errorf("%s is a standalone printer inventory; it already holds printers", path)
	}
	if hasKey(root, SectionKey) {
		return fmt.Errorf("%s already has a `%s:` section; nothing was changed", path, SectionKey)
	}
	return nil
}

// attach hangs inventory off root as the printer section.
func attach(root, inventory *yaml.Node, comment string) {
	key := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: SectionKey, HeadComment: comment}
	root.Content = append(root.Content, key, inventory)
}

func samePath(a, b string) (bool, error) {
	absA, err := filepath.Abs(a)
	if err != nil {
		return false, err
	}
	absB, err := filepath.Abs(b)
	if err != nil {
		return false, err
	}
	if infoA, errA := os.Stat(absA); errA == nil {
		if infoB, errB := os.Stat(absB); errB == nil {
			return os.SameFile(infoA, infoB), nil
		}
	}
	return filepath.Clean(absA) == filepath.Clean(absB), nil
}
