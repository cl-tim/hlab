package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/cl-tim/hlab/internal/printerlab/migrate"
	"github.com/cl-tim/hlab/internal/printerlab/moonraker"
)

func newKlippeeBoxAutoInsertCmd(a *app, enable bool) *cobra.Command {
	name := "box-auto-insert-disable"
	description := "Disable automatic QIDI Box filament insertion"
	script := "QIDI_BOX_AUTO_INSERT_DISABLE"
	want := false
	if enable {
		name = "box-auto-insert-enable"
		description = "Enable automatic QIDI Box filament insertion"
		script = "QIDI_BOX_AUTO_INSERT_ENABLE"
		want = true
	}
	return &cobra.Command{
		Use:   name + " PRINTER",
		Short: description,
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireApproval("QIDI Box automatic-insertion setting change"); err != nil {
				return err
			}
			printer, err := a.onePrinter(args[0])
			if err != nil {
				return err
			}
			if err := migrate.RequirePlus4(printer); err != nil {
				return err
			}
			client, err := moonraker.New(printer, a.timeout())
			if err != nil {
				return fmt.Errorf("%s: %w", printer.Name, err)
			}
			if err := client.EnsureIdle(cmd.Context()); err != nil {
				return fmt.Errorf("%s: automatic-insertion change blocked: %w", printer.Name, err)
			}
			before, err := client.BoxGuardStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: cannot verify QIDI Box guard before change: %w", printer.Name, err)
			}
			if !before.ReloadAllGuarded {
				return fmt.Errorf("%s: RELOAD_ALL automatic-insertion guard is not active", printer.Name)
			}
			if err := client.RunGcode(cmd.Context(), script); err != nil {
				return fmt.Errorf("%s: automatic-insertion change failed: %w", printer.Name, err)
			}
			after, err := client.BoxGuardStatus(cmd.Context())
			if err != nil {
				return fmt.Errorf("%s: cannot verify QIDI Box automatic-insertion setting: %w", printer.Name, err)
			}
			if !after.AutoInsertVariablePresent || after.AutoInsertEnabled != want {
				return fmt.Errorf("%s: QIDI Box automatic-insertion setting was not verified", printer.Name)
			}
			state := "disabled"
			if want {
				state = "enabled"
			}
			_, err = fmt.Fprintf(a.out, "%s: guarded QIDI Box automatic insertion %s and verified.\n", printer.Name, state)
			return err
		},
	}
}
