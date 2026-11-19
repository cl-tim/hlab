package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/stack"
)

// nameLine matches the compose document's own project name declaration.
var nameLine = regexp.MustCompile(`(?m)^name:.*$`)

func newStackRenameCmd(a *app) *cobra.Command {
	var (
		apply bool
		force bool
	)

	cmd := &cobra.Command{
		Use:   "rename <host> <old> <new>",
		Short: "Rename a stack hlab manages",
		Long: "Renames a stack: its directory under " + stack.RemoteDir + " moves, the compose\n" +
			"file is rewritten to declare the new name, and it is started again.\n\n" +
			"Compose derives network and volume names from the project name, so a\n" +
			"rename is not free. Bind-mounted data travels with the directory and is\n" +
			"unaffected, but a **named volume** is prefixed with the project name and\n" +
			"would be left behind — the stack would come up against an empty new one\n" +
			"and look like it worked. A stack declaring named volumes is refused unless\n" +
			"you pass --force and have moved the data yourself.\n\n" +
			"If the stack does not come up under the new name it is put back.\n\n" +
			"Nothing is changed unless --apply is given.",
		Example: "  hlab stack rename media-1 tim media\n" +
			"  hlab stack rename media-1 tim media --apply",
		Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}
			oldName, newName := args[1], args[2]

			projects, err := a.listProjects(cmd.Context(), host)
			if err != nil {
				return err
			}
			var project *stack.Project
			for i := range projects {
				if projects[i].Name == oldName {
					project = &projects[i]
				}
			}
			if project == nil {
				return fmt.Errorf("%s has no compose project called %q", host.Name, oldName)
			}
			if !project.Adopted() {
				return fmt.Errorf(
					"%s is not managed by hlab (it lives at %s).\nAdopt it first: hlab stack adopt %s %s",
					oldName, project.Dir(), host.Name, oldName)
			}

			local := filepath.Join(config.Home(), stack.LocalDirName, oldName, "compose.yaml")
			compose, err := os.ReadFile(local)
			if err != nil {
				return fmt.Errorf(
					"read %s: %w\nRun `hlab stack pull %s %s` first", local, err, host.Name, oldName)
			}

			// Named volumes are prefixed with the project name, so they do not
			// follow a rename. Data is not destroyed, but the stack starts
			// against an empty volume — which is worse than an error, because
			// nothing announces it.
			volumes, err := stack.NamedVolumes(compose)
			if err != nil {
				return err
			}

			fmt.Fprintf(a.out, "Rename %q -> %q on %s\n", oldName, newName, host.Name)
			fmt.Fprintf(a.out, "  move:    %s/%s -> %s/%s\n",
				stack.RemoteDir, oldName, stack.RemoteDir, newName)
			if len(volumes) > 0 {
				fmt.Fprintf(a.out, "  volumes: %s (named — these do NOT follow the rename)\n",
					strings.Join(volumes, ", "))
				if !force {
					return fmt.Errorf(
						"refusing: %s declares named volume(s) %s.\n"+
							"Compose prefixes them with the project name, so the renamed stack would start\n"+
							"against empty ones and look healthy. Move the data first, then pass --force",
						oldName, strings.Join(volumes, ", "))
				}
				fmt.Fprintln(a.errOut, "  --force given; the named volumes are your problem")
			} else {
				fmt.Fprintln(a.out, "  volumes: none named, so nothing is left behind")
			}

			renamed := setProjectName(compose, newName)
			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			script, err := stack.BuildRenameScript(oldName, newName, renamed)
			if err != nil {
				return err
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			res, err := runPrivilegedOn(cmd.Context(), dialer, targetFor(host), script)
			if err != nil {
				return err
			}

			if err := a.reportAdoption(
				res.Stdout, res.Stderr, res.ExitCode, host.Name, newName,
				stack.AdoptOptions{Name: newName, SourceDir: stack.RemoteDir + "/" + oldName},
			); err != nil {
				return err
			}

			// Keep the local copy in step, or the next deploy writes the old
			// name back over the machine.
			if err := moveLocalStack(oldName, newName, renamed); err != nil {
				fmt.Fprintf(a.errOut, "  the machine was renamed but the local copy was not: %v\n", err)
			}
			return nil
		},
	}

	f := cmd.Flags()
	f.BoolVar(&apply, "apply", false, "actually rename; without it nothing is changed")
	f.BoolVar(&force, "force", false, "rename even though named volumes will be left behind")
	return cmd
}

// setProjectName rewrites, or adds, the compose document's own name.
//
// Stating it in the file rather than letting it come from the directory is what
// keeps the project's identity from changing again the next time anything moves.
func setProjectName(compose []byte, name string) []byte {
	if nameLine.Match(compose) {
		return nameLine.ReplaceAll(compose, []byte("name: "+name))
	}
	return append([]byte("name: "+name+"\n\n"), compose...)
}

// moveLocalStack renames the reviewed copy hlab keeps.
func moveLocalStack(oldName, newName string, compose []byte) error {
	root := filepath.Join(config.Home(), stack.LocalDirName)
	newDir := filepath.Join(root, newName)

	if err := os.MkdirAll(newDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(newDir, "compose.yaml"), compose, 0o600); err != nil {
		return err
	}
	return os.RemoveAll(filepath.Join(root, oldName))
}
