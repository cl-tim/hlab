package stages

import (
	"testing"

	"github.com/cl-tim/homelab-cli/internal/printerlab/probe"
)

func find(t *testing.T, decisions []Decision, id string) Decision {
	t.Helper()
	for _, decision := range decisions {
		if decision.Stage == id {
			return decision
		}
	}
	t.Fatalf("no decision for stage %q", id)
	return Decision{}
}

// allSatisfied is a printer that observably completed every verifiable stage.
func allSatisfied() map[string]probe.Status {
	statuses := map[string]probe.Status{}
	for _, stage := range Graph() {
		for _, id := range stage.Verify {
			statuses[id] = probe.Satisfied
		}
	}
	return statuses
}

func completedAll() map[string]Attempt {
	attempts := map[string]Attempt{}
	for _, stage := range Graph() {
		attempts[stage.ID] = AttemptCompleted
	}
	return attempts
}

// --------------------------------------------------------------------------
// Graph shape.
// --------------------------------------------------------------------------

// Reconcile resolves dependencies in a single forward pass, so a stage that
// appeared before its own requirement would be silently mis-blocked.
func TestGraphIsTopologicallyOrdered(t *testing.T) {
	seen := map[string]bool{}
	for _, stage := range Graph() {
		for _, required := range stage.Requires {
			if !seen[required] {
				t.Errorf("stage %q requires %q, which does not appear before it",
					stage.ID, required)
			}
		}
		seen[stage.ID] = true
	}
}

func TestGraphIdentifiersAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, stage := range Graph() {
		if seen[stage.ID] {
			t.Errorf("duplicate stage id %q", stage.ID)
		}
		seen[stage.ID] = true
	}
}

func TestGraphRequirementsExist(t *testing.T) {
	for _, stage := range Graph() {
		for _, required := range append(append([]string{}, stage.Requires...), stage.SupersededBy...) {
			if _, ok := Find(Graph(), required); !ok {
				t.Errorf("stage %q references unknown stage %q", stage.ID, required)
			}
		}
	}
}

// Both toolhead flashes run tooling that only bootstrap-host installs:
// st-flash/st-info come from stlink-tools, and flashtool.py from the katapult
// clone. plab's older `mainline plan` ordered them before bootstrap, which
// cannot work on the printer.
func TestToolheadFlashingDependsOnTheBootstrappedHost(t *testing.T) {
	for _, id := range []string{"flash-toolhead-katapult", "flash-toolhead"} {
		stage, ok := Find(Graph(), id)
		if !ok {
			t.Fatalf("missing stage %q", id)
		}
		if !dependsOn(t, stage.ID, "bootstrap-host") {
			t.Errorf("%s must depend on bootstrap-host", stage.ID)
		}
	}
}

func dependsOn(t *testing.T, from, target string) bool {
	t.Helper()
	stage, ok := Find(Graph(), from)
	if !ok {
		return false
	}
	for _, required := range stage.Requires {
		if required == target || dependsOn(t, required, target) {
			return true
		}
	}
	return false
}

func TestDigestChangesWithTheSequence(t *testing.T) {
	base := Digest(Graph())
	if base != Digest(Graph()) {
		t.Fatal("digest is not stable across calls")
	}
	altered := Graph()
	altered[0].Requires = append(altered[0].Requires, "build")
	if Digest(altered) == base {
		t.Fatal("digest did not change when a requirement was added")
	}
}

// --------------------------------------------------------------------------
// The reconciliation table.
// --------------------------------------------------------------------------

func TestConvergedPrinterSkipsEverything(t *testing.T) {
	decisions := Reconcile(Graph(), State{
		Attempts: completedAll(), Probes: allSatisfied(),
	})

	for _, decision := range decisions {
		if decision.Action != ActionSkip {
			t.Errorf("stage %s: got %s, want skip (%s)",
				decision.Stage, decision.Action, decision.Reason)
		}
		// Stages that own a probe must be confirmed by it. Stages without one
		// are necessarily accepted on the journal's word.
		stage, _ := Find(Graph(), decision.Stage)
		if len(stage.Verify) > 0 && decision.Unverified {
			t.Errorf("stage %s has probes but was skipped without confirmation",
				decision.Stage)
		}
	}
	if _, found := Next(decisions); found {
		t.Fatal("a fully converged printer should have no next stage")
	}
}

// A stage recorded as complete whose effect has vanished means something undid
// it. Continuing past that would build on an assumption the printer denies.
func TestCompletedButUnsatisfiedStops(t *testing.T) {
	probes := allSatisfied()
	probes["bootstrap.receipt"] = probe.Unsatisfied

	decisions := Reconcile(Graph(), State{Attempts: completedAll(), Probes: probes})

	decision := find(t, decisions, "bootstrap-host")
	if decision.Action != ActionStop {
		t.Fatalf("got %s, want stop: %s", decision.Action, decision.Reason)
	}
}

func TestCompletedButUnconfirmedIsSkippedAndFlagged(t *testing.T) {
	decisions := Reconcile(Graph(), State{
		Attempts: completedAll(), Probes: map[string]probe.Status{},
	})

	decision := find(t, decisions, "bootstrap-host")
	if decision.Action != ActionSkip || !decision.Unverified {
		t.Fatalf("got %s unverified=%v, want skip flagged unverified",
			decision.Action, decision.Unverified)
	}
}

// Someone migrated the machine by hand, or an earlier run predates the journal.
func TestUnattemptedButSatisfiedConverges(t *testing.T) {
	decisions := Reconcile(Graph(), State{
		Attempts: map[string]Attempt{}, Probes: allSatisfied(),
	})

	decision := find(t, decisions, "bootstrap-host")
	if decision.Action != ActionSkip {
		t.Fatalf("got %s, want skip: %s", decision.Action, decision.Reason)
	}
}

// The dangerous case: the tool died part way through something destructive.
func TestStartedDestructiveStageStops(t *testing.T) {
	attempts := completedAll()
	attempts["flash-toolhead-katapult"] = AttemptStarted

	decisions := Reconcile(Graph(), State{Attempts: attempts, Probes: allSatisfied()})

	decision := find(t, decisions, "flash-toolhead-katapult")
	if decision.Action != ActionStop {
		t.Fatalf("got %s, want stop: %s", decision.Action, decision.Reason)
	}
}

func TestStartedSafeStageIsRepeated(t *testing.T) {
	attempts := completedAll()
	attempts["backup"] = AttemptStarted
	probes := allSatisfied()

	decisions := Reconcile(Graph(), State{Attempts: attempts, Probes: probes})

	if decision := find(t, decisions, "backup"); decision.Action != ActionRun {
		t.Fatalf("got %s, want run: %s", decision.Action, decision.Reason)
	}
}

// A crash after the work but before the record is resolved by the probe.
func TestStartedButVerifiedIsAccepted(t *testing.T) {
	attempts := completedAll()
	attempts["bootstrap-host"] = AttemptStarted

	decisions := Reconcile(Graph(), State{Attempts: attempts, Probes: allSatisfied()})

	if decision := find(t, decisions, "bootstrap-host"); decision.Action != ActionSkip {
		t.Fatalf("got %s, want skip: %s", decision.Action, decision.Reason)
	}
}

func TestUnattemptedIndeterminateRunsOnlyWhenSafeToRepeat(t *testing.T) {
	// tune-host is safe to repeat, so an unknown state may simply be re-run.
	attempts := completedAll()
	delete(attempts, "tune-host")
	probes := allSatisfied()
	probes["host-tuning.installed"] = probe.Indeterminate

	decisions := Reconcile(Graph(), State{Attempts: attempts, Probes: probes})
	if decision := find(t, decisions, "tune-host"); decision.Action != ActionRun {
		t.Fatalf("tune-host: got %s, want run: %s", decision.Action, decision.Reason)
	}
}

func TestDestructiveStageBehindUnconfirmedPrerequisiteStops(t *testing.T) {
	// bootstrap-host is accepted on the journal's word alone, then the erase
	// stage is asked to run behind it.
	attempts := completedAll()
	delete(attempts, "flash-toolhead-katapult")
	delete(attempts, "flash-toolhead")

	decisions := Reconcile(Graph(), State{
		Attempts: attempts, Probes: map[string]probe.Status{},
	})

	bootstrap := find(t, decisions, "bootstrap-host")
	if !bootstrap.Unverified {
		t.Fatalf("expected bootstrap-host to be flagged unverified, got %+v", bootstrap)
	}
	erase := find(t, decisions, "flash-toolhead-katapult")
	if erase.Action != ActionStop {
		t.Fatalf("got %s, want stop: %s", erase.Action, erase.Reason)
	}
}

// Both toolhead flashes are unprobeable by design. If "no probe exists" were
// escalated the same way as "the probe went unanswered", flash-toolhead could
// never follow flash-toolhead-katapult and every migration would deadlock.
func TestDestructiveStageBehindAnUnprobeablePrerequisiteStillRuns(t *testing.T) {
	attempts := completedAll()
	delete(attempts, "flash-toolhead")

	decisions := Reconcile(Graph(), State{Attempts: attempts, Probes: allSatisfied()})

	katapult := find(t, decisions, "flash-toolhead-katapult")
	if !katapult.Unverified {
		t.Fatal("flash-toolhead-katapult has no probe and should be flagged unverified")
	}
	decision := find(t, decisions, "flash-toolhead")
	if decision.Action != ActionRun {
		t.Fatalf("got %s, want run: %s", decision.Action, decision.Reason)
	}
}

func TestUnmetRequirementBlocks(t *testing.T) {
	decisions := Reconcile(Graph(), State{
		Attempts: map[string]Attempt{}, Probes: map[string]probe.Status{},
	})

	// Nothing has run, so the first stage is runnable and the rest wait.
	if decision := find(t, decisions, "setup-ssh"); decision.Action != ActionRun {
		t.Fatalf("setup-ssh: got %s, want run", decision.Action)
	}
	decision := find(t, decisions, "backup")
	if decision.Action != ActionBlocked {
		t.Fatalf("backup: got %s, want blocked", decision.Action)
	}
	if len(decision.Blocking) == 0 {
		t.Fatal("a blocked stage should name what it is waiting on")
	}
}

// The inert smoke-test config is deliberately replaced by the real one, so its
// probe must stop being read as a regression once that has happened.
func TestSupersededStageIsNotARegression(t *testing.T) {
	probes := allSatisfied()
	probes["config.smoke-test"] = probe.Unsatisfied
	probes["config.installed"] = probe.Satisfied

	decisions := Reconcile(Graph(), State{Attempts: completedAll(), Probes: probes})

	decision := find(t, decisions, "smoke-test")
	if decision.Action != ActionSkip {
		t.Fatalf("got %s, want skip: %s", decision.Action, decision.Reason)
	}
}

// --------------------------------------------------------------------------
// Probe aggregation.
// --------------------------------------------------------------------------

func TestOneUnsatisfiedProbeFailsTheWholeStage(t *testing.T) {
	stage, _ := Find(Graph(), "bootstrap-host")
	statuses := map[string]probe.Status{}
	for _, id := range stage.Verify {
		statuses[id] = probe.Satisfied
	}
	statuses[stage.Verify[0]] = probe.Unsatisfied

	if got := verify(stage, statuses); got != VerifiedUnsatisfied {
		t.Fatalf("got %s, want unsatisfied", got)
	}
}

func TestUnsatisfiedOutranksIndeterminate(t *testing.T) {
	stage, _ := Find(Graph(), "bootstrap-host")
	statuses := map[string]probe.Status{
		stage.Verify[0]: probe.Unsatisfied,
	}

	if got := verify(stage, statuses); got != VerifiedUnsatisfied {
		t.Fatalf("got %s, want unsatisfied", got)
	}
}

// An older firmware repository that does not know a probe must not be able to
// manufacture a negative verdict from its silence.
func TestUnknownProbeIsIndeterminateNotNegative(t *testing.T) {
	stage, _ := Find(Graph(), "bootstrap-host")

	if got := verify(stage, map[string]probe.Status{}); got != VerifiedIndeterminate {
		t.Fatalf("got %s, want indeterminate", got)
	}
}

func TestStageWithoutProbesReportsNone(t *testing.T) {
	stage, _ := Find(Graph(), "backup")

	if got := verify(stage, allSatisfied()); got != VerifiedNone {
		t.Fatalf("got %s, want none", got)
	}
}

// --------------------------------------------------------------------------
// Next.
// --------------------------------------------------------------------------

func TestNextPrefersAnEarlierStopOverALaterRun(t *testing.T) {
	decisions := []Decision{
		{Order: 1, Stage: "a", Action: ActionSkip},
		{Order: 2, Stage: "b", Action: ActionStop},
		{Order: 3, Stage: "c", Action: ActionRun},
	}

	decision, found := Next(decisions)
	if !found || decision.Stage != "b" {
		t.Fatalf("got %+v, want the stop at stage b", decision)
	}
}

func TestImplLookupIgnoresOperatorPerformedStages(t *testing.T) {
	if _, ok := ImplFor(Graph(), ""); ok {
		t.Fatal("an empty impl must not match the operator-performed stage")
	}
	if id, ok := ImplFor(Graph(), "bootstrap-host"); !ok || id != "bootstrap-host" {
		t.Fatalf("got %q %v, want bootstrap-host", id, ok)
	}
}
