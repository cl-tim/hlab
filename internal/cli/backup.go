package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/backup"
	"github.com/cl-tim/homelab-cli/internal/config"
	"github.com/cl-tim/homelab-cli/internal/inventory"
	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/sshx"
)

// backupDirName is where backups are kept under hlab's home.
const backupDirName = "backups"

func newBackupCmd(a *app) *cobra.Command {
	var (
		out        string
		self       bool
		includeKey bool
	)

	cmd := &cobra.Command{
		Use:   "backup [selector]",
		Short: "Copy the state hlab keeps on each machine back to here",
		Long: "Archives " + backup.RemoteDir + " from every selected machine — the compose\n" +
			"files hlab deployed and, more importantly, the data beside them: a DNS\n" +
			"zone, a DHCP lease database, service credentials, printer configuration.\n\n" +
			"Bulk data mounted from elsewhere on the machine is deliberately not\n" +
			"included. A media library is large, is not hlab's, and backing it up here\n" +
			"would turn a cheap safety net into something nobody runs.\n\n" +
			"The machine checksums what it wrote and hlab verifies it after the\n" +
			"transfer. An archive that arrives short is refused now rather than at the\n" +
			"moment somebody needs it.\n\n" +
			"Archives are written under hlab's home, not the working directory, for the\n" +
			"same reason the management key is: they contain secrets.",
		Example: "  hlab backup\n  hlab backup apps-1\n  hlab backup --out D:\\\\backups",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			hosts, err := a.managedHosts(args)
			if err != nil {
				return err
			}

			root := out
			if root == "" {
				root = filepath.Join(config.Home(), backupDirName)
			}
			stamp := time.Now().Format("20060102-150405")

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}

			tbl := output.Table{Headers: []string{"HOST", "FILES", "SIZE", "ARCHIVE"}}
			var failed, saved int

			for _, h := range hosts {
				path, arc, err := a.backupHost(cmd.Context(), dialer, h, root, stamp)
				switch {
				case errors.Is(err, backup.ErrNothing):
					// Most machines in a fleet run nothing hlab deployed.
					tbl.AddRow(h.Name, "-", "-", "nothing hlab manages here")
				case err != nil:
					failed++
					tbl.AddRow(h.Name, "-", "-", "failed: "+firstNonEmptyLine(err.Error()))
				default:
					saved++
					tbl.AddRow(h.Name, fmt.Sprint(arc.Files), humanBytes(len(arc.Data)), path)
				}
			}

			if err := output.WriteTable(a.out, tbl); err != nil {
				return err
			}
			if failed > 0 {
				fmt.Fprintf(a.errOut, "\n%d machine(s) could not be backed up.\n", failed)
				return &ExitError{Code: 1}
			}
			fmt.Fprintf(a.errOut, "\nVerified archive readability and the checksum each machine computed.\n")

			if self {
				path, err := a.backupSelf(root, stamp, includeKey)
				if err != nil {
					return err
				}
				fmt.Fprintf(a.out, "\nhlab's own inventory -> %s\n", path)
				if includeKey {
					fmt.Fprintln(a.errOut,
						"The management key is in that archive. It is root-equivalent on every\n"+
							"enrolled machine, so wherever this folder syncs to now holds the fleet.")
				} else {
					fmt.Fprintln(a.errOut,
						"The inventory is included; the management key is not. Add --include-key to\n"+
							"add it, knowing it is root-equivalent on every enrolled machine.")
				}
			}
			return nil
		},
	}

	cmd.AddCommand(newBackupVerifyCmd(a), newRecoveryDrillCmd(a))
	f := cmd.Flags()
	f.StringVar(&out, "out", "", "directory to write into (default: hlab's home)")
	f.BoolVar(&self, "self", false,
		"also archive hlab's own inventory, which a replacement machine is rebuilt from")
	f.BoolVar(&includeKey, "include-key", false,
		"include the management key in the --self archive; it is root-equivalent on every machine")
	return cmd
}

// backupHost pulls one machine's archive and writes it out.
func (a *app) backupHost(
	ctx context.Context, dialer *sshx.Dialer, h inventory.Host, root, stamp string,
) (string, backup.Archive, error) {
	res, err := runPrivilegedOn(ctx, dialer, targetFor(h), backup.BuildArchiveScript())
	if err != nil {
		return "", backup.Archive{}, err
	}

	arc, err := backup.ParseArchive(h.Name, res.Stdout)
	if err != nil {
		return "", backup.Archive{}, err
	}

	if err := checkArchiveReadable(arc.Data); err != nil {
		return "", backup.Archive{}, fmt.Errorf("read downloaded archive: %w", err)
	}

	dir := filepath.Join(root, h.Name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", backup.Archive{}, fmt.Errorf("create %s: %w", dir, err)
	}

	path := filepath.Join(dir, stamp+".tar.gz")
	// 0600: the archive contains the secrets the machine holds.
	if err := os.WriteFile(path, arc.Data, 0o600); err != nil {
		return "", backup.Archive{}, fmt.Errorf("write %s: %w", path, err)
	}
	// The checksum is stored beside the archive so a restore can prove the file
	// on disk is still the one that was taken.
	sumPath := path + ".sha256"
	if err := os.WriteFile(sumPath, []byte(arc.SHA256+"\n"), 0o600); err != nil {
		return "", backup.Archive{}, fmt.Errorf("write %s: %w", sumPath, err)
	}

	return path, arc, nil
}

func newRestoreCmd(a *app) *cobra.Command {
	var (
		from  string
		apply bool
	)

	cmd := &cobra.Command{
		Use:   "restore <host>",
		Short: "Put a machine's backed-up state back",
		Long: "Sends a backup archive to a machine and unpacks it over " + backup.RemoteDir + ".\n\n" +
			"By default the most recent archive taken from that same machine is used;\n" +
			"--from names a different one, including one taken from another machine if\n" +
			"you are rebuilding onto replacement hardware.\n\n" +
			"The machine verifies the archive's checksum before touching anything, stops\n" +
			"the stacks so their data does not move underneath them, keeps the current\n" +
			"state aside under /var/lib/hlab, unpacks, and starts them again. A restore\n" +
			"from the wrong archive is therefore something you can walk back.\n\n" +
			"This overwrites live service data. Nothing happens without --apply.",
		Example: "  hlab restore apps-1\n  hlab restore apps-1 --apply",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			host, err := a.oneManagedHost(args)
			if err != nil {
				return err
			}

			path := from
			if path == "" {
				path, err = latestBackup(filepath.Join(config.Home(), backupDirName, host.Name))
				if err != nil {
					return err
				}
			}

			data, err := os.ReadFile(path)
			if err != nil {
				return fmt.Errorf("read %s: %w", path, err)
			}
			sum, err := verifyStoredArchive(path, data)
			if err != nil {
				return err
			}

			fmt.Fprintf(a.out, "Restore %s -> %s\n", path, host.Name)
			fmt.Fprintf(a.out, "  archive: %s, sha256 %s\n", humanBytes(len(data)), sum[:16]+"...")
			fmt.Fprintf(a.out, "  unpacks over: %s (current state kept aside first)\n", backup.RemoteDir)
			fmt.Fprintln(a.out, "  stacks are stopped and started again around it")

			if !apply {
				fmt.Fprintln(a.out, "\nNot applied. Re-run with --apply.")
				return nil
			}

			dialer, err := a.newDialer()
			if err != nil {
				return err
			}
			return a.restoreHost(cmd.Context(), dialer, host, data, sum)
		},
	}

	f := cmd.Flags()
	f.StringVar(&from, "from", "", "archive to restore (default: the newest taken from this machine)")
	f.BoolVar(&apply, "apply", false, "actually restore; without it nothing is changed")
	return cmd
}

// restoreHost stages the archive on the machine and unpacks it.
func (a *app) restoreHost(
	ctx context.Context, dialer *sshx.Dialer, h inventory.Host, data []byte, sum string,
) error {
	client, err := dialer.Dial(ctx, targetFor(h))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()

	// The archive travels as the command's input. Sending it inside the script
	// would mean quoting twenty megabytes of payload into a shell word.
	encoded := base64.StdEncoding.EncodeToString(data)
	staged, err := sshx.Exec(ctx, client, backup.StageCommand, bytes.NewReader([]byte(encoded)))
	if err != nil {
		return fmt.Errorf("send the archive: %w", err)
	}
	if !staged.OK() {
		return fmt.Errorf("send the archive: %s", firstNonEmptyLine(staged.Stderr))
	}

	script, err := backup.BuildRestoreScript(sum, time.Now().Format("20060102-150405"))
	if err != nil {
		return err
	}

	res, err := runPrivilegedOn(ctx, dialer, targetFor(h), script)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(res.Stdout, "\n") {
		if kept, ok := strings.CutPrefix(strings.TrimSpace(line), "kept:"); ok {
			fmt.Fprintf(a.errOut, "  previous state kept at %s\n", kept)
		}
		if started, ok := strings.CutPrefix(strings.TrimSpace(line), "started:"); ok {
			fmt.Fprintf(a.errOut, "  started %s\n", started)
		}
	}
	if !strings.Contains(res.Stdout, backup.MarkerRestored) {
		msg := firstNonEmptyLine(res.Stderr)
		if msg == "" {
			msg = fmt.Sprintf("the restore did not report success (exit %d)", res.ExitCode)
		}
		return fmt.Errorf("%s", strings.TrimPrefix(msg, "hlab:error "))
	}

	w := a.out
	if a.format.Structured() {
		w = a.errOut
	}
	fmt.Fprintf(w, "\nRestored %s. Run `hlab sync %s` to record what is now running.\n", h.Name, h.Name)
	return nil
}

// latestBackup finds the newest archive taken from a machine.
func latestBackup(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", fmt.Errorf("no backups found in %s; run `hlab backup` first", dir)
	}

	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".tar.gz") {
			names = append(names, e.Name())
		}
	}
	if len(names) == 0 {
		return "", fmt.Errorf("no archives in %s; run `hlab backup` first", dir)
	}

	// Names are timestamps, so the last in order is the newest.
	sort.Strings(names)
	return filepath.Join(dir, names[len(names)-1]), nil
}

// verifyStoredArchive checks the archive on disk against the checksum recorded
// beside it, so a file damaged at rest is caught before it is sent anywhere.
func verifyStoredArchive(path string, data []byte) (string, error) {
	want, err := os.ReadFile(path + ".sha256")
	if err != nil {
		return "", fmt.Errorf(
			"no checksum beside %s, so it cannot be proved intact; restore it by hand if you are sure", path)
	}

	got := sha256Hex(data)
	if trimmed := strings.TrimSpace(string(want)); trimmed != got {
		return "", fmt.Errorf(
			"%s does not match its checksum (%s vs %s); it has been damaged since it was taken",
			path, got, trimmed)
	}
	return got, nil
}

func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// sha256Hex is the digest of a byte slice, hex encoded.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
