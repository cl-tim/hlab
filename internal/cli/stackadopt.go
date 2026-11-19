package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/config"
	"github.com/cl-tim/hlab/internal/stack"
)

func newStackAdoptCmd(a *app) *cobra.Command {
	var (
		file   string
		withFn []string
		apply  bool
	)

	cmd := &cobra.Command{
		Use:   "adopt <host> <project>",
		Short: "Move a hand-made compose project under hlab's management",
		Long: "Takes a project that was set up by hand and makes it one of hlab's: the\n" +
			"whole directory moves to " + stack.RemoteDir + "/<project>, and the compose file\n" +
			"reviewed locally is deployed in place of the one that was there.\n\n" +
			"The directory is moved rather than copied, because a stack's relative bind\n" +
			"mounts are its data. A compose file relocated without ./appdata beside it\n" +
			"starts cleanly and comes up empty, which is worse than failing.\n\n" +
			"The project keeps its name, so the networks and volumes compose derives\n" +
			"from it keep theirs. Run `hlab stack pull` first, read what you got, and\n" +
			"edit it — that file is what gets deployed.\n\n" +
			"If the stack does not come up, the original compose file and location are\n" +
			"restored and it is started again where it was.\n\n" +
			"Nothing is changed unless --apply is given.",
		Example: "  hlab stack pull apps-1 apps\n" +
			"  hlab stack adopt apps-1 apps\n" +
			"  hlab stack adopt apps-1 apps --apply",
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args[:1])
			if err != nil {
				return err
			}
			name := args[1]

			projects, err := a.listProjects(cmd.Context(), host)
			if err != nil {
				return err
			}

			var project *stack.Project
			for i := range projects {
				if projects[i].Name == name {
					project = &projects[i]
				}
			}
			if project == nil {
				return fmt.Errorf("%s has no compose project called %q", host.Name, name)
			}
			if project.Adopted() {
				return fmt.Errorf("%s is already managed by hlab, at %s", name, project.Dir())
			}

			local := file
			if local == "" {
				local = filepath.Join(config.Home(), stack.LocalDirName, name, "compose.yaml")
			}
			compose, err := os.ReadFile(local)
			if err != nil {
				return fmt.Errorf(
					"read %s: %w\nRun `hlab stack pull %s %s` first, then review the file",
					local, err, host.Name, name)
			}

			// A file that renames the project would orphan the networks and
			// volumes compose derived from the old name, and the running stack
			// would quietly become a second one.
			if declared := stack.ProjectName(compose); declared != "" && declared != name {
				return fmt.Errorf(
					"%s declares `name: %s`, but the project on %s is %q.\n"+
						"Adopting it would create a second project and orphan the first one's data",
					local, declared, host.Name, name)
			}

			assets, err := readAssets(withFn)
			if err != nil {
				return err
			}

			files := project.Files()
			original := ""
			if len(files) > 0 {
				original = path.Base(files[0])
			}

			// Whether the directory moves is decided by the compose file, not
			// by a flag: a stack that keeps nothing beside itself has nothing
			// to carry, and moving its directory would be gratuitous — often
			// actively wrong, since a project defined straight in a home
			// directory reports that home as its project directory.
			mounts, err := stack.RelativeBindMounts(compose)
			if err != nil {
				return err
			}

			opts := stack.AdoptOptions{
				Name:         name,
				SourceDir:    project.Dir(),
				OriginalFile: original,
				Compose:      compose,
				Assets:       assets,
				CarriesData:  len(mounts) > 0,
			}

			if err := a.describeAdoption(host.Name, local, opts, compose); err != nil {
				return err
			}
			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			script, err := stack.BuildAdoptScript(opts)
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

			return a.reportAdoption(res.Stdout, res.Stderr, res.ExitCode, host.Name, name, opts)
		},
	}

	f := cmd.Flags()
	f.StringVar(&file, "file", "", "compose file to deploy (default: the pulled copy in hlab's home)")
	f.StringArrayVar(&withFn, "with", nil, "extra file to deploy alongside it; repeatable")
	f.BoolVar(&apply, "apply", false, "actually adopt; without it nothing is changed")
	return cmd
}

// describeAdoption prints what the move would do before it is done.
func (a *app) describeAdoption(
	hostName, local string, opts stack.AdoptOptions, compose []byte,
) error {
	fmt.Fprintf(a.out, "Adopt %q on %s\n", opts.Name, hostName)

	mounts, err := stack.RelativeBindMounts(compose)
	if err != nil {
		return err
	}
	if opts.CarriesData {
		fmt.Fprintf(a.out, "  move:    %s -> %s\n", opts.SourceDir, stack.RemoteDir+"/"+opts.Name)
		var sources []string
		for _, m := range mounts {
			sources = append(sources, m.Source)
		}
		fmt.Fprintf(a.out, "  carries: %s\n", strings.Join(sources, ", "))
	} else {
		fmt.Fprintf(a.out, "  deploy:  %s -> %s\n", opts.Name, stack.RemoteDir+"/"+opts.Name)
		fmt.Fprintf(a.out, "  leaves:  %s alone (nothing is kept beside the compose file)\n",
			opts.SourceDir)
	}
	fmt.Fprintf(a.out, "  from:    %s\n", local)

	ports, err := stack.PublishedPorts(compose)
	if err != nil {
		return err
	}
	if len(ports) == 0 {
		fmt.Fprintln(a.out, "  ports:   none published")
		return nil
	}
	var claims []string
	for _, p := range ports {
		claims = append(claims, fmt.Sprintf("%d (%s)", p.Port, p.Service))
	}
	fmt.Fprintf(a.out, "  ports:   %s\n", strings.Join(claims, ", "))
	return nil
}

// reportAdoption turns the script's markers into an account of what happened.
//
// The stages are reported separately because they fail differently: a stack
// that stopped but did not move is still where it was, and one that moved but
// did not come up has been put back. Saying only "failed" would leave an
// operator guessing which.
func (a *app) reportAdoption(
	stdout, stderr string, exitCode int, hostName, name string, opts stack.AdoptOptions,
) error {
	if strings.Contains(stdout, stack.MarkerNoDocker) {
		return fmt.Errorf("%s has no docker installed", hostName)
	}

	switch {
	case strings.Contains(stdout, stack.MarkerDeployed):
		fmt.Fprintf(a.out, "\nAdopted: %s now lives in %s/%s\n", name, stack.RemoteDir, name)
		states := stack.ParseContainerStates(stdout)
		for _, s := range states {
			fmt.Fprintf(a.out, "  %-28s %-16s %s\n", s.Name, s.Service, s.State)
		}
		fmt.Fprintf(a.errOut, "\nRun `hlab sync %s` to record what is now running.\n", hostName)
		return nil

	case strings.Contains(stdout, stack.MarkerRolled):
		return fmt.Errorf(
			"the stack did not come up, so it was put back at %s and restarted there.\n"+
				"Nothing was lost. Fix the compose file and try again: %s",
			opts.SourceDir, firstNonEmptyLine(stderr))

	case strings.Contains(stdout, stack.MarkerMoved):
		return fmt.Errorf(
			"the project was moved to %s/%s but the deploy did not finish, and it could\n"+
				"not be put back. Its data is intact in that directory: %s",
			stack.RemoteDir, name, firstNonEmptyLine(stderr))

	case strings.Contains(stdout, stack.MarkerStopped):
		return fmt.Errorf(
			"the stack was stopped but not moved, so it is still at %s.\n"+
				"Start it again with `docker compose up -d` there: %s",
			opts.SourceDir, firstNonEmptyLine(stderr))
	}

	msg := firstNonEmptyLine(stderr)
	if msg == "" {
		msg = fmt.Sprintf("adopt exited %d without reporting success", exitCode)
	}
	return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
}
