package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/cl-tim/homelab-cli/internal/output"
	"github.com/cl-tim/homelab-cli/internal/printerlab/config"
	"github.com/cl-tim/homelab-cli/internal/printerlab/inventory"
	"github.com/cl-tim/homelab-cli/internal/printerlab/journal"
	"github.com/cl-tim/homelab-cli/internal/printerlab/migrate"
	"github.com/cl-tim/homelab-cli/internal/printerlab/probe"
	"github.com/cl-tim/homelab-cli/internal/printerlab/stages"
)

const (
	recipeName           = "plus4-mainline"
	defaultConfigProfile = "mainline-klipper-beacon"
)

func (a *app) migrationDefaults() config.MigrationDefaults {
	if a.cfg == nil {
		return config.MigrationDefaults{}
	}
	return a.cfg.Defaults.Migration
}

type engineReport struct {
	Printer         string            `json:"printer" yaml:"printer"`
	Recipe          string            `json:"recipe" yaml:"recipe"`
	RecipeDigest    string            `json:"recipeDigest" yaml:"recipeDigest"`
	Reachable       bool              `json:"reachable" yaml:"reachable"`
	SequenceChanged bool              `json:"sequenceChanged,omitempty" yaml:"sequenceChanged,omitempty"`
	Stages          []stages.Decision `json:"stages" yaml:"stages"`
	Probes          []probe.Result    `json:"probes,omitempty" yaml:"probes,omitempty"`
}

// statuses indexes live checks for reconciliation and dry-run simulation.
func (r engineReport) statuses() map[string]probe.Status {
	statuses := make(map[string]probe.Status, len(r.Probes))
	for _, result := range r.Probes {
		statuses[result.Probe] = result.Status
	}
	return statuses
}

func (a *app) journalFor(printer inventory.Printer) (*journal.Journal, error) {
	graph := stages.Graph()
	path := journal.Path(config.Home(), printer.Name)
	return journal.Load(path, printer.Name, recipeName, stages.Digest(graph))
}

// reconcile combines journal records with current printer state.
func (a *app) reconcile(ctx context.Context, printer inventory.Printer) (engineReport, error) {
	graph := stages.Graph()
	digest := stages.Digest(graph)

	book, err := a.journalFor(printer)
	if err != nil {
		return engineReport{}, err
	}
	if book.Stale(digest) {
		_, _ = fmt.Fprintf(a.errOut,
			"WARNING: %s's journal uses a different migration sequence; review recorded stages\n", printer.Name)
	}

	client := probe.Client{Runner: a.migrationRunner()}
	report, err := client.Collect(ctx, printer)
	if err != nil {
		return engineReport{}, err
	}

	decisions := stages.Reconcile(graph, stages.State{
		Attempts: preparationAttempts(book, printer, time.Now()),
		Probes:   report.Statuses(),
	})
	return engineReport{
		Printer:         printer.Name,
		Recipe:          recipeName,
		RecipeDigest:    digest,
		Reachable:       report.Reachable,
		SequenceChanged: book.Stale(digest),
		Stages:          decisions,
		Probes:          report.Probes,
	}, nil
}

// Expire stock preparation records after 24 hours, up to the first cutover stage.
// After cutover, retain the original recovery evidence.
func preparationAttempts(book *journal.Journal, printer inventory.Printer, now time.Time) map[string]stages.Attempt {
	attempts := book.Attempts()
	if printer.Firmware != inventory.FirmwareStock {
		return attempts
	}
	for _, id := range []string{"stage-mainboard", "bootstrap-host", "flash-toolhead-katapult", "flash-toolhead"} {
		if attempt := attempts[id]; attempt != "" && attempt != stages.AttemptNone {
			return attempts
		}
	}
	for _, id := range []string{"audit", "backup"} {
		entry := book.Entries[id]
		if entry.Attempt == stages.AttemptCompleted && (entry.Finished.IsZero() || now.Sub(entry.Finished) > 24*time.Hour) {
			attempts[id] = stages.AttemptNone
		}
	}
	return attempts
}

// runStage saves the stage's start before execution, then records its result.
// An interrupted stage retains its started record for reconciliation.
func (a *app) runStage(
	ctx context.Context,
	printer inventory.Printer,
	impl string,
	args ...string,
) error {
	if err := migrate.ValidateConnectionArguments(printer, args); err != nil {
		return err
	}
	stageID, known := stages.ImplFor(stages.Graph(), impl)
	if !known {
		return a.migrationRunner().Run(ctx, args...)
	}
	book, err := a.journalFor(printer)
	if err != nil {
		return err
	}
	book.Start(stageID)
	if err := book.Save(); err != nil {
		return fmt.Errorf("record start of %s: %w", stageID, err)
	}
	runErr := a.migrationRunner().Run(ctx, args...)
	if runErr != nil {
		book.Fail(stageID, runErr)
	} else {
		book.Complete(stageID)
	}
	if err := book.Save(); err != nil {
		_, _ = fmt.Fprintf(a.errOut,
			"WARNING: %s finished but its outcome could not be recorded: %v\n", stageID, err)
	}
	return runErr
}

// runWorkstationStage handles the stages that produce local artifacts and take
// no printer argument. Attribution to a printer's journal is opt-in through
// --for, since one build can serve several machines.
func (a *app) runWorkstationStage(
	ctx context.Context,
	forPrinter, impl string,
	args ...string,
) error {
	if forPrinter == "" {
		return a.migrationRunner().Run(ctx, args...)
	}
	printer, err := a.onePrinter(forPrinter)
	if err != nil {
		return err
	}
	if err := migrate.RequirePlus4(printer); err != nil {
		return err
	}
	return a.runStage(ctx, printer, impl, args...)
}

func newMainlineStateCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "state PRINTER",
		Short: "Show migration progress and live checks",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			report, err := a.reconcile(cmd.Context(), printer)
			if err != nil {
				return err
			}
			if !report.Reachable {
				_, _ = fmt.Fprintf(a.errOut,
					"%s is unreachable; showing journal progress with indeterminate live checks\n", printer.Name)
			}
			table := output.Table{
				Headers: []string{"ORDER", "STAGE", "ACTION", "RECORDED", "VERIFIED", "REASON"},
			}
			for _, decision := range report.Stages {
				table.AddRow(decision.Order, decision.Stage, decision.Action,
					decision.Attempt, decision.Verification, decision.Reason)
			}
			return output.Write(a.out, a.format, report, table)
		},
	}
}

func newMainlineNextCmd(a *app) *cobra.Command {
	return &cobra.Command{
		Use:   "next PRINTER",
		Short: "Show the next migration stage",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			report, err := a.reconcile(cmd.Context(), printer)
			if err != nil {
				return err
			}
			decision, found := stages.Next(report.Stages)
			if !found {
				type complete struct {
					Printer string `json:"printer" yaml:"printer"`
					Action  string `json:"action" yaml:"action"`
					Reason  string `json:"reason" yaml:"reason"`
				}
				done := complete{printer.Name, "none", "every stage is satisfied"}
				table := output.Table{Headers: []string{"PRINTER", "ACTION", "REASON"}}
				table.AddRow(done.Printer, done.Action, done.Reason)
				return output.Write(a.out, a.format, done, table)
			}

			table := output.Table{Headers: []string{"FIELD", "VALUE"}}
			table.AddRow("stage", decision.Stage)
			table.AddRow("action", decision.Action)
			table.AddRow("recorded", decision.Attempt)
			table.AddRow("verified", decision.Verification)
			table.AddRow("reason", decision.Reason)
			if stage, ok := stages.Find(stages.Graph(), decision.Stage); ok {
				if stage.Impl != "" {
					command := stageCommand(stage, printer)
					table.AddRow("command", command)
				} else {
					table.AddRow("command", "(performed by the operator)")
				}
			}
			if decision.Manual != "" {
				table.AddRow("physical", decision.Manual)
			}
			if err := output.Write(a.out, a.format, decision, table); err != nil {
				return err
			}
			// A stop is not a runnable next step; it needs a person.
			if decision.Action == stages.ActionStop {
				return &ExitError{Code: 1}
			}
			return nil
		},
	}
}

func newMainlineSetupUserCmd(a *app) *cobra.Command {
	var newUser, pubkey, viaUser, viaIdentity string
	command := &cobra.Command{
		Use:   "setup-user PRINTER",
		Short: "Create a dedicated key-only management account with passwordless sudo",
		Long: "Create an SSH-key management account with passwordless sudo.\n" +
			"Use --via-user and --via-identity to connect through an existing account\n" +
			"that has passwordless sudo.",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			if err := a.requireApproval("management user creation"); err != nil {
				return err
			}
			if viaUser != "" {
				printer.User = viaUser
			}
			if viaIdentity != "" {
				printer.IdentityFile = viaIdentity
			}
			// Use the bootstrap account's password environment for this connection.
			printer.PrinterUser = ""
			stageArgs := append([]string{"setup-management-user"},
				migrate.ConnectionArgs(printer)...)
			stageArgs = append(stageArgs,
				"--new-user", newUser,
				"--pubkey", pubkey,
				"--confirm", "CREATE-MANAGEMENT-USER",
			)
			if err := a.migrationRunner().Run(cmd.Context(), stageArgs...); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(a.errOut,
				"\nSet `user: %s` for %s in the inventory, and keep `printerUser` "+
					"on the account that owns Klipper.\n", newUser, printer.Name)
			return nil
		},
	}
	command.Flags().StringVar(&newUser, "new-user", "plab", "account to create")
	command.Flags().StringVar(&pubkey, "pubkey", "", "OpenSSH public key to install")
	command.Flags().StringVar(&viaUser, "via-user", "",
		"connect as this existing privileged account instead of the inventory's user")
	command.Flags().StringVar(&viaIdentity, "via-identity", "",
		"SSH key for the --via-user connection")
	_ = command.MarkFlagRequired("pubkey")
	return command
}

func newMainlineMarkCmd(a *app) *cobra.Command {
	var state string
	command := &cobra.Command{
		Use:   "mark PRINTER STAGE",
		Short: "Record a migration stage's status",
		Long: "Record a stage's completion or failure in the migration journal.\n" +
			"Live checks validate the recorded state when migration resumes.",
		Args: cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			graph := stages.Graph()
			stage, ok := stages.Find(graph, args[1])
			if !ok {
				return fmt.Errorf("unknown stage %q; run `hlab printer firmware advanced plan %s` for the sequence",
					args[1], printer.Name)
			}
			attempt := stages.Attempt(state)
			switch attempt {
			case stages.AttemptCompleted, stages.AttemptFailed, stages.AttemptNone:
			default:
				return fmt.Errorf(
					"--state must be one of %s, %s, or %s",
					stages.AttemptCompleted, stages.AttemptFailed, stages.AttemptNone)
			}

			book, err := a.journalFor(printer)
			if err != nil {
				return err
			}
			switch attempt {
			case stages.AttemptCompleted:
				book.Start(stage.ID)
				book.Complete(stage.ID)
			case stages.AttemptFailed:
				book.Start(stage.ID)
				book.Fail(stage.ID, fmt.Errorf("marked failed by the operator"))
			case stages.AttemptNone:
				delete(book.Entries, stage.ID)
			}
			book.Adopt(stages.Digest(graph))
			if err := book.Save(); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(a.out, "Recorded %s as %s for %s\n",
				stage.ID, attempt, printer.Name)
			return nil
		},
	}
	command.Flags().StringVar(&state, "state", string(stages.AttemptCompleted),
		"journal state to record: completed, failed, or unattempted")
	return command
}
