// Package stages defines the Plus4 migration graph and resume decisions.
// It combines workstation journal records with live printer checks.
// Disagreements stop migration; journal records support stages performed
// while the host or MCU is unavailable.
package stages

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/cl-tim/hlab/internal/printerlab/probe"
)

// Rerun states whether repeating a stage is safe once it may already have run.
type Rerun string

const (
	RerunSafe      Rerun = "safe"
	RerunConfirm   Rerun = "requires-confirmation"
	RerunForbidden Rerun = "forbidden"
)

// Attempt is what the journal recorded for a stage.
type Attempt string

const (
	AttemptNone      Attempt = "unattempted"
	AttemptStarted   Attempt = "started"
	AttemptCompleted Attempt = "completed"
	AttemptFailed    Attempt = "failed"
)

// Verification is the aggregate of a stage's probes.
type Verification string

const (
	VerifiedSatisfied     Verification = "satisfied"
	VerifiedUnsatisfied   Verification = "unsatisfied"
	VerifiedIndeterminate Verification = "indeterminate"
	VerifiedNone          Verification = "none"
)

// Action is what the engine decided to do with a stage.
type Action string

const (
	ActionSkip    Action = "skip"
	ActionRun     Action = "run"
	ActionBlocked Action = "blocked"
	ActionStop    Action = "stop"
)

type Stage struct {
	ID      string
	Summary string
	// Impl is the plab subcommand that performs the stage. An empty Impl marks
	// a stage the operator performs by hand, which the engine can verify but
	// never execute.
	Impl         string
	Requires     []string
	Verify       []string
	SupersededBy []string
	Confirm      string
	Rerun        Rerun
	Manual       string
}

func (s Stage) Destructive() bool { return s.Confirm != "" }

type Decision struct {
	Order        int          `json:"order" yaml:"order"`
	Stage        string       `json:"stage" yaml:"stage"`
	Action       Action       `json:"action" yaml:"action"`
	Attempt      Attempt      `json:"attempt" yaml:"attempt"`
	Verification Verification `json:"verification" yaml:"verification"`
	Reason       string       `json:"reason" yaml:"reason"`
	// Unverified marks a stage accepted on journal evidence alone. A later
	// destructive stage refuses to run behind one of these.
	Unverified bool     `json:"unverified,omitempty" yaml:"unverified,omitempty"`
	Blocking   []string `json:"blocking,omitempty" yaml:"blocking,omitempty"`
	Manual     string   `json:"manual,omitempty" yaml:"manual,omitempty"`
}

// State is the evidence reconciliation runs against.
type State struct {
	Attempts map[string]Attempt
	Probes   map[string]probe.Status
}

// Graph returns the Plus4 migration sequence.
// Toolhead flashing depends on bootstrap-host for ST-Link and Katapult tools.
func Graph() []Stage {
	return []Stage{
		{
			ID: "setup-ssh", Impl: "setup-ssh", Rerun: RerunSafe,
			Summary: "Install a management SSH key",
		},
		{
			// Audit completion records the operator's review in the journal.
			ID: "audit", Impl: "audit", Requires: []string{"setup-ssh"},
			Rerun:   RerunSafe,
			Summary: "Read-only live printer audit",
		},
		{
			ID: "backup", Impl: "backup", Requires: []string{"audit"},
			Rerun:   RerunSafe,
			Summary: "Verified pre-migration backup",
		},
		{
			ID: "build", Impl: "build", Rerun: RerunSafe,
			Summary: "Build and validate all MCU artifacts",
		},
		{
			ID: "stage-mainboard", Impl: "stage-mainboard",
			Requires: []string{"build"}, Rerun: RerunSafe,
			Summary: "Stage qd_mcu.bin on FAT32 microSD",
			Manual:  "Insert the microSD card and power cycle; the bootloader renames qd_mcu.bin to qd_mcu.CUR",
		},
		{
			ID: "bootstrap-host", Impl: "bootstrap-host",
			Requires: []string{"backup", "stage-mainboard"},
			Rerun:    RerunSafe, Confirm: "BOOTSTRAP-HOST",
			Verify: []string{
				"bootstrap.receipt", "bootstrap.sources",
				"bootstrap.services", "bootstrap.uart-claimed",
			},
			Summary: "Install upstream Klipper, Moonraker and Katapult",
			Manual:  "Replace the host eMMC with the official Armbian image before bootstrapping",
		},
		{
			ID: "flash-toolhead-katapult", Impl: "flash-toolhead-katapult",
			Requires: []string{"build", "bootstrap-host"},
			Rerun:    RerunConfirm, Confirm: "ERASE-TOOLHEAD",
			Summary: "Erase the toolhead and install Katapult",
			Manual:  "Connect an ST-Link to the toolhead SWD header",
		},
		{
			ID: "flash-toolhead", Impl: "flash-toolhead",
			Requires: []string{"flash-toolhead-katapult"},
			Rerun:    RerunConfirm, Confirm: "FLASH-TOOLHEAD",
			Summary: "Flash mainline Klipper through Katapult",
			Manual:  "Disconnect the ST-Link and restore the toolhead wiring",
		},
		{
			ID: "tune-host", Impl: "tune-host", Requires: []string{"bootstrap-host"},
			Rerun: RerunSafe, Confirm: "TUNE-HOST",
			Verify:  []string{"host-tuning.installed"},
			Summary: "Install persistent RK3328 host latency tuning",
		},
		{
			ID: "smoke-test", Impl: "smoke-test",
			Requires: []string{"bootstrap-host", "flash-toolhead"},
			Rerun:    RerunSafe, Confirm: "SMOKE-TEST",
			Verify: []string{"config.smoke-test", "mcu.online"},
			// The inert config is deliberately replaced by the real one, so a
			// completed smoke test stops being observable once it is.
			SupersededBy: []string{"install-config"},
			Summary:      "Install the inert two-MCU smoke-test configuration",
		},
		{
			ID: "render-config", Impl: "render-config", Rerun: RerunSafe,
			Summary: "Render a reviewed mainline printer configuration",
		},
		{
			ID: "install-config", Requires: []string{"smoke-test", "render-config"},
			Rerun: RerunSafe, Verify: []string{"config.installed"},
			Summary: "Install the reviewed configuration and commission the printer",
			Manual: "Review the rendered configuration, copy it to the printer " +
				"and restart Klipper; plab does not install it for you",
		},
	}
}

// Digest fingerprints the graph so a journal cannot be resumed against a
// sequence that has since changed underneath it.
func Digest(graph []Stage) string {
	var builder strings.Builder
	for _, stage := range graph {
		fmt.Fprintf(&builder, "%s|%s|%s|%s|%s|%s|%s\n",
			stage.ID, stage.Impl, stage.Confirm, stage.Rerun,
			strings.Join(stage.Requires, ","),
			strings.Join(stage.Verify, ","),
			strings.Join(stage.SupersededBy, ","))
	}
	sum := sha256.Sum256([]byte(builder.String()))
	return hex.EncodeToString(sum[:])
}

func Find(graph []Stage, id string) (Stage, bool) {
	for _, stage := range graph {
		if stage.ID == id {
			return stage, true
		}
	}
	return Stage{}, false
}

// ImplFor maps a plab subcommand back to its stage id.
func ImplFor(graph []Stage, impl string) (string, bool) {
	for _, stage := range graph {
		if stage.Impl != "" && stage.Impl == impl {
			return stage.ID, true
		}
	}
	return "", false
}

// verify aggregates stage checks: unsatisfied takes precedence over unknown.
// Missing checks count as indeterminate.
func verify(stage Stage, statuses map[string]probe.Status) Verification {
	if len(stage.Verify) == 0 {
		return VerifiedNone
	}
	unknown := false
	for _, id := range stage.Verify {
		switch statuses[id] {
		case probe.Unsatisfied:
			return VerifiedUnsatisfied
		case probe.Satisfied:
		default:
			unknown = true
		}
	}
	if unknown {
		return VerifiedIndeterminate
	}
	return VerifiedSatisfied
}

// decide reconciles a journal attempt with its live verification result.
func decide(stage Stage, attempt Attempt, verification Verification) (Action, string, bool) {
	safe := stage.Rerun == RerunSafe
	switch attempt {
	case AttemptCompleted:
		switch verification {
		case VerifiedSatisfied:
			return ActionSkip, "recorded complete and confirmed on the printer", false
		case VerifiedUnsatisfied:
			return ActionStop, "recorded complete but the printer no longer shows its " +
				"effect; something undid this stage", false
		case VerifiedNone:
			return ActionSkip, "recorded complete; this stage has no probe to confirm it", true
		default:
			return ActionSkip, "recorded complete; the printer could not confirm it", true
		}
	case AttemptNone:
		switch verification {
		case VerifiedSatisfied:
			return ActionSkip, "already present on the printer although plab did not run it", false
		case VerifiedUnsatisfied:
			return ActionRun, "not run and not present on the printer", false
		case VerifiedIndeterminate:
			if safe {
				return ActionRun, "not run; state unconfirmed but re-running is safe", false
			}
			return ActionStop, "not run and the printer could not confirm its state; " +
				"this stage is not safe to repeat blindly", false
		default:
			return ActionRun, "not run", false
		}
	case AttemptStarted:
		if verification == VerifiedSatisfied {
			return ActionSkip, "an attempt did not record completion but the printer " +
				"confirms its effect", false
		}
		if safe {
			return ActionRun, "a previous attempt did not finish; re-running is safe", false
		}
		// The tool died part way through something that cannot simply be
		// repeated. This is the case the journal exists to catch.
		return ActionStop, "a previous attempt started and never finished; " +
			"inspect the hardware before repeating this stage", false
	case AttemptFailed:
		if safe {
			return ActionRun, "a previous attempt failed; re-running is safe", false
		}
		return ActionStop, "a previous attempt failed and this stage cannot be " +
			"repeated without inspection", false
	default:
		return ActionStop, fmt.Sprintf("unrecognised journal state %q", attempt), false
	}
}

// Reconcile walks the graph in order and decides each stage.
func Reconcile(graph []Stage, state State) []Decision {
	verifications := make(map[string]Verification, len(graph))
	for _, stage := range graph {
		verifications[stage.ID] = verify(stage, state.Probes)
	}

	decisions := make([]Decision, 0, len(graph))
	done := make(map[string]bool, len(graph))
	// unconfirmed tracks stages whose checks returned indeterminate.
	// Stages without checks rely on their journal records.
	unconfirmed := make(map[string]bool, len(graph))

	for index, stage := range graph {
		attempt := state.Attempts[stage.ID]
		if attempt == "" {
			attempt = AttemptNone
		}
		verification := verifications[stage.ID]

		var action Action
		var reason string
		var acceptedOnRecord bool
		if superseded, by := supersededBy(stage, verifications); superseded {
			action, reason, acceptedOnRecord = ActionSkip, "superseded by "+by, false
		} else {
			action, reason, acceptedOnRecord = decide(stage, attempt, verification)
		}

		decision := Decision{
			Order: index + 1, Stage: stage.ID, Action: action, Attempt: attempt,
			Verification: verification, Reason: reason, Unverified: acceptedOnRecord,
			Manual: stage.Manual,
		}

		// A stage whose effect is already present needs no prerequisites: the
		// machine may have been migrated by hand or by an earlier run.
		if action != ActionSkip {
			if unmet := missing(stage.Requires, done); len(unmet) > 0 {
				decision.Action = ActionBlocked
				decision.Blocking = unmet
				decision.Reason = "waiting on " + strings.Join(unmet, ", ")
			}
		}

		// Never let a destructive stage run behind a prerequisite whose own
		// probe went unanswered. The printer was asked and could not say, so
		// the ground under this stage is genuinely unknown.
		if decision.Action == ActionRun && stage.Destructive() {
			if soft := softRequirements(stage.Requires, unconfirmed); len(soft) > 0 {
				decision.Action = ActionStop
				decision.Reason = "requires " + strings.Join(soft, ", ") +
					", which the printer could not confirm; verify by hand before a destructive stage"
			}
		}

		if decision.Action == ActionSkip {
			done[stage.ID] = true
			unconfirmed[stage.ID] = verification == VerifiedIndeterminate
		}
		decisions = append(decisions, decision)
	}
	return decisions
}

func supersededBy(stage Stage, verifications map[string]Verification) (bool, string) {
	for _, id := range stage.SupersededBy {
		if verifications[id] == VerifiedSatisfied {
			return true, id
		}
	}
	return false, ""
}

func missing(requires []string, done map[string]bool) []string {
	var unmet []string
	for _, id := range requires {
		if !done[id] {
			unmet = append(unmet, id)
		}
	}
	sort.Strings(unmet)
	return unmet
}

func softRequirements(requires []string, unverified map[string]bool) []string {
	var soft []string
	for _, id := range requires {
		if unverified[id] {
			soft = append(soft, id)
		}
	}
	sort.Strings(soft)
	return soft
}

// Next returns the first stage requiring action.
// A stopped stage blocks later stages.
func Next(decisions []Decision) (Decision, bool) {
	for _, decision := range decisions {
		if decision.Action == ActionStop || decision.Action == ActionRun {
			return decision, true
		}
	}
	return Decision{}, false
}
