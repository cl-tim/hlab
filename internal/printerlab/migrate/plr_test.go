package migrate

import (
	"strings"
	"testing"
)

func plrBool(value bool) *bool { return &value }

const testPLRTargetJSON = `
		"account":"mks",
		"paths":{
			"home":"/home/mks",
			"klipper_dir":"/home/mks/klipper",
			"printer_data":"/home/mks/printer_data",
			"scripts_dir":"/home/mks/scripts/plr",
			"python":"/home/mks/klippy-env/bin/python",
			"gcode_root":"/home/mks/printer_data/gcodes",
			"checkpoint":"/home/mks/scripts/plr/plr_checkpoint.json",
			"record":"/home/mks/scripts/plr/plr_record",
			"output":"/home/mks/printer_data/gcodes/.plr/plr.gcode"
		},`

func TestParsePLROperationRequiresOneExpectedDocument(t *testing.T) {
	manifest := "/home/mks/scripts/plr/backups/plr-hardening-x/backup-manifest.json"
	raw := []byte(`{
		"schema":"qidi-plus4-plr-deployment/v1",
		` + testPLRTargetJSON + `
		"ok":true,
		"transaction_state":"applied",
		"changed":true,
		"applied":true,
		"verified":true,
		"klipper_left_stopped":true,
		"backup_manifest":"` + manifest + `",
		"installation":{"backup_manifest":"` + manifest + `"}
	}`)
	result, err := ParsePLROperation(raw, PLRDeploymentSchema)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupManifest != manifest {
		t.Fatalf("backup manifest = %q", result.BackupManifest)
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}

	for name, document := range map[string][]byte{
		"wrong schema":  []byte(`{"schema":"wrong","ok":true}`),
		"two documents": append(append([]byte{}, raw...), []byte(` {}`)...),
		"conflicting manifests": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"backup_manifest":"one","installation":{"backup_manifest":"two"}
		}`),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePLROperation(document, PLRDeploymentSchema); err == nil {
				t.Fatal("expected rejection")
			}
		})
	}
}

func TestPLRDiagnosticSummaryIncludesRemoteInstallerJSONError(t *testing.T) {
	result, err := ParsePLROperation([]byte(`{
		"schema":"qidi-plus4-plr-deployment/v1","ok":false,
		"phase":"apply","error":"Remote PLR installer exited 1",
		"installation":{
			"error":"installation failed and rollback was attempted: owner mismatch",
			"transaction_state":"auto_rolled_back"
		}
	}`), PLRDeploymentSchema)
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := result.DiagnosticSummary()
	for _, wanted := range []string{
		"phase apply", "Remote PLR installer exited 1",
		"installer: installation failed and rollback was attempted: owner mismatch",
	} {
		if !strings.Contains(diagnostic, wanted) {
			t.Fatalf("diagnostic %q missing %q", diagnostic, wanted)
		}
	}
}

func TestValidatePLRLifecycleRejectsMissingProof(t *testing.T) {
	result := PLROperationResult{
		Schema:           PLRDeploymentSchema,
		OK:               plrBool(true),
		TransactionState: PLRTransactionApplied,
		Changed:          plrBool(true),
		Applied:          plrBool(true),
		Verified:         plrBool(true),
	}
	if err := ValidatePLRLifecycle(result); err == nil {
		t.Fatal("expected stopped-state proof rejection")
	}

	zero := 0
	// Failed results remain renderable, but they must still identify the fixed
	// service and carry explicit (including zero-valued) execution proof.
	result = PLROperationResult{
		Schema: KlipperStartSchema, OK: plrBool(false), Service: KlipperService,
		ObservedState: "failed", StartExit: &zero, StateExit: &zero,
		StabilityChecks: 2, StabilityInterval: 1,
		StabilitySamples: []PLRStabilitySample{{State: "failed", Exit: &zero}},
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}
	result.StartExit = nil
	if err := ValidatePLRLifecycle(result); err == nil {
		t.Fatal("expected missing start_exit rejection")
	}
}

func TestValidatePLRLifecycleRequiresBoundedStartStabilityProof(t *testing.T) {
	zero := 0
	result := PLROperationResult{
		Schema: KlipperStartSchema, OK: plrBool(true), Service: KlipperService,
		ObservedState: "active", StartExit: &zero, StateExit: &zero,
		StabilityChecks: 2, StabilityInterval: 1,
		StabilitySamples: []PLRStabilitySample{
			{State: "active", Exit: &zero},
			{State: "active", Exit: &zero},
		},
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}
	result.StabilitySamples = result.StabilitySamples[:1]
	if err := ValidatePLRLifecycle(result); err == nil {
		t.Fatal("expected incomplete stability proof rejection")
	}
}

func TestValidatePLRLifecycleRequiresStartServiceAndExplicitZeroProof(t *testing.T) {
	valid := []byte(`{
		"schema":"qidi-plus4-klipper-start/v1","ok":true,
		"service":"klipper.service","observed_state":"active",
		"start_exit":0,"state_exit":0,
		"stability_checks_required":2,"stability_interval_seconds":1,
		"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}]
	}`)
	result, err := ParsePLROperation(valid, KlipperStartSchema)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}

	for name, document := range map[string][]byte{
		"wrong service": []byte(`{
			"schema":"qidi-plus4-klipper-start/v1","ok":true,
			"service":"moonraker.service","observed_state":"active",
			"start_exit":0,"state_exit":0,
			"stability_checks_required":2,"stability_interval_seconds":1,
			"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}]
		}`),
		"missing start exit": []byte(`{
			"schema":"qidi-plus4-klipper-start/v1","ok":true,
			"service":"klipper.service","observed_state":"active","state_exit":0,
			"stability_checks_required":2,"stability_interval_seconds":1,
			"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}]
		}`),
		"missing state exit": []byte(`{
			"schema":"qidi-plus4-klipper-start/v1","ok":true,
			"service":"klipper.service","observed_state":"active","start_exit":0,
			"stability_checks_required":2,"stability_interval_seconds":1,
			"stability_samples":[{"state":"active","exit":0},{"state":"active","exit":0}]
		}`),
		"missing sample exit": []byte(`{
			"schema":"qidi-plus4-klipper-start/v1","ok":true,
			"service":"klipper.service","observed_state":"active",
			"start_exit":0,"state_exit":0,
			"stability_checks_required":2,"stability_interval_seconds":1,
			"stability_samples":[{"state":"active"},{"state":"active","exit":0}]
		}`),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParsePLROperation(document, KlipperStartSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePLRLifecycle(result); err == nil {
				t.Fatal("expected proof rejection")
			}
		})
	}
}

func TestParsePLROperationPromotesStructuredFailureBackupManifest(t *testing.T) {
	manifest := "/home/mks/scripts/plr/backups/failed/backup-manifest.json"
	result, err := ParsePLROperation([]byte(`{
		"schema":"qidi-plus4-plr-deployment/v1","ok":false,
		"transaction_state":"partial",
		"changed":true,"applied":null,"verified":false,
		"klipper_left_stopped":true,
		"error":"post-install verification failed",
		"installation":{"backup_manifest":"`+manifest+`"}
	}`), PLRDeploymentSchema)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupManifest != manifest {
		t.Fatalf("backup manifest = %q, want %q", result.BackupManifest, manifest)
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}
}

func TestValidatePLRLifecycleRequiresPresentBooleansAndExactUnchangedInvariants(t *testing.T) {
	validDeployment := []byte(`{
		"schema":"qidi-plus4-plr-deployment/v1","ok":true,
		"transaction_state":"not_started",
		"changed":false,"applied":false,"verified":true,
		"klipper_left_stopped":false
	}`)
	validRollback := []byte(`{
		"schema":"qidi-plus4-plr-rollback/v1","ok":true,
		"transaction_state":"not_started",
		"changed":false,"applied":false,"verified":true,
		"klipper_left_stopped":false,
		"backup_manifest":"/home/mks/scripts/plr/backups/exact/backup-manifest.json"
	}`)
	for name, test := range map[string]struct {
		document []byte
		schema   string
	}{
		"deployment": {validDeployment, PLRDeploymentSchema},
		"rollback":   {validRollback, PLRRollbackSchema},
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParsePLROperation(test.document, test.schema)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePLRLifecycle(result); err != nil {
				t.Fatal(err)
			}
		})
	}

	for name, document := range map[string][]byte{
		"missing ok": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1",
			"transaction_state":"not_started",
			"changed":false,"applied":false,"verified":true,
			"klipper_left_stopped":false
		}`),
		"missing changed": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"transaction_state":"not_started",
			"applied":false,"verified":true,"klipper_left_stopped":false
		}`),
		"unchanged but applied": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"transaction_state":"not_started",
			"changed":false,"applied":true,"verified":true,
			"klipper_left_stopped":false
		}`),
		"unchanged but unverified": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"transaction_state":"not_started",
			"changed":false,"applied":false,"verified":false,
			"klipper_left_stopped":false
		}`),
		"unchanged but stopped": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"transaction_state":"not_started",
			"changed":false,"applied":false,"verified":true,
			"klipper_left_stopped":true
		}`),
		"unchanged deployment with manifest": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":true,
			"transaction_state":"not_started",
			"changed":false,"applied":false,"verified":true,
			"klipper_left_stopped":false,"backup_manifest":"unexpected"
		}`),
		"unchanged rollback without manifest": []byte(`{
			"schema":"qidi-plus4-plr-rollback/v1","ok":true,
			"transaction_state":"not_started",
			"changed":false,"applied":false,"verified":true,
			"klipper_left_stopped":false
		}`),
	} {
		t.Run(name, func(t *testing.T) {
			schema := PLRDeploymentSchema
			if name == "unchanged rollback without manifest" {
				schema = PLRRollbackSchema
			}
			result, err := ParsePLROperation(document, schema)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePLRLifecycle(result); err == nil {
				t.Fatal("expected lifecycle proof rejection")
			}
		})
	}
}

func TestValidatePLRTargetRequiresInventoryAccountCanonicalPathsAndHandles(t *testing.T) {
	paths, err := CanonicalPLRPaths("mks")
	if err != nil {
		t.Fatal(err)
	}
	result := PLROperationResult{
		Account: "mks", Paths: paths,
		BackupManifest: "/home/mks/scripts/plr/backups/" +
			"plr-hardening-20260806T000000Z-test/backup-manifest.json",
		SafetyManifest: "/home/mks/scripts/plr/backups/" +
			"plr-rollback-safety-20260806T000000Z-random/safety-manifest.json",
	}
	if err := ValidatePLRTarget(result, "mks"); err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*PLROperationResult){
		"wrong account": func(result *PLROperationResult) { result.Account = "root" },
		"wrong scripts": func(result *PLROperationResult) {
			result.Paths.ScriptsDir = "/home/root/scripts/plr"
		},
		"escaped backup": func(result *PLROperationResult) {
			result.BackupManifest = "/home/mks/scripts/plr/backups/plr-hardening-x/../backup-manifest.json"
		},
		"wrong safety filename": func(result *PLROperationResult) {
			result.SafetyManifest = "/home/mks/scripts/plr/backups/plr-rollback-safety-x/backup-manifest.json"
		},
	} {
		t.Run(name, func(t *testing.T) {
			candidate := result
			mutate(&candidate)
			if err := ValidatePLRTarget(candidate, "mks"); err == nil {
				t.Fatal("expected target proof rejection")
			}
		})
	}
	if _, err := CanonicalPLRPaths("../root"); err == nil {
		t.Fatal("expected unsafe account rejection")
	}
}

func TestParsePLROperationRequiresExactPathFieldSet(t *testing.T) {
	for name, paths := range map[string]string{
		"missing": `{"home":"/home/mks"}`,
		"extra": `{
			"home":"/home/mks","klipper_dir":"/home/mks/klipper",
			"printer_data":"/home/mks/printer_data","scripts_dir":"/home/mks/scripts/plr",
			"python":"/home/mks/klippy-env/bin/python","gcode_root":"/home/mks/printer_data/gcodes",
			"checkpoint":"/home/mks/scripts/plr/plr_checkpoint.json",
			"record":"/home/mks/scripts/plr/plr_record",
			"output":"/home/mks/printer_data/gcodes/.plr/plr.gcode","unexpected":"value"
		}`,
	} {
		t.Run(name, func(t *testing.T) {
			document := []byte(`{"schema":"qidi-plus4-plr-deployment/v1","paths":` + paths + `}`)
			if _, err := ParsePLROperation(document, PLRDeploymentSchema); err == nil {
				t.Fatal("expected exact path schema rejection")
			}
		})
	}
}

func TestValidatePLRLifecycleModelsPartialAndCompensatedFailures(t *testing.T) {
	manifest := "/home/mks/scripts/plr/backups/plr-hardening-x/backup-manifest.json"
	for _, state := range []string{PLRTransactionPartial, PLRTransactionIndeterminate} {
		document := []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"` + state + `","changed":true,
			"applied":null,"verified":false,"klipper_left_stopped":true,
			"backup_manifest":"` + manifest + `"
		}`)
		result, err := ParsePLROperation(document, PLRDeploymentSchema)
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePLRLifecycle(result); err != nil {
			t.Fatalf("%s: %v", state, err)
		}
	}

	for name, document := range map[string][]byte{
		"partial false instead of null": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"partial","changed":true,"applied":false,
			"verified":false,"klipper_left_stopped":true,
			"backup_manifest":"` + manifest + `"
		}`),
		"partial omitted applied": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"partial","changed":true,
			"verified":false,"klipper_left_stopped":true,
			"backup_manifest":"` + manifest + `"
		}`),
		"partial omitted recovery": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"partial","changed":true,"applied":null,
			"verified":false,"klipper_left_stopped":true
		}`),
		"compensated true": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"auto_rolled_back","changed":true,"applied":true,
			"verified":false,"klipper_left_stopped":true,
			"backup_manifest":"` + manifest + `"
		}`),
	} {
		t.Run(name, func(t *testing.T) {
			result, err := ParsePLROperation(document, PLRDeploymentSchema)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePLRLifecycle(result); err == nil {
				t.Fatal("expected transaction proof rejection")
			}
		})
	}
}

func TestParsePLROperationPromotesAndConflictChecksFailureHandles(t *testing.T) {
	backup := "/home/mks/scripts/plr/backups/plr-hardening-x/backup-manifest.json"
	safety := "/home/mks/scripts/plr/backups/plr-rollback-safety-x/safety-manifest.json"
	result, err := ParsePLROperation([]byte(`{
		"schema":"qidi-plus4-plr-rollback/v1","ok":false,
		"transaction_state":"partial","changed":true,"applied":null,
		"verified":false,"klipper_left_stopped":true,
		"rollback":{"backup_manifest":"`+backup+`","safety_manifest":"`+safety+`"}
	}`), PLRRollbackSchema)
	if err != nil {
		t.Fatal(err)
	}
	if result.BackupManifest != backup || result.SafetyManifest != safety {
		t.Fatalf("promoted handles = %q, %q", result.BackupManifest, result.SafetyManifest)
	}
	if err := ValidatePLRLifecycle(result); err != nil {
		t.Fatal(err)
	}
	for name, document := range map[string][]byte{
		"rollback indeterminate without safety": []byte(`{
			"schema":"qidi-plus4-plr-rollback/v1","ok":false,
			"transaction_state":"indeterminate","changed":true,"applied":null,
			"verified":false,"klipper_left_stopped":true,
			"backup_manifest":"` + backup + `"
		}`),
		"deploy not started with backup": []byte(`{
			"schema":"qidi-plus4-plr-deployment/v1","ok":false,
			"transaction_state":"not_started","changed":false,"applied":false,
			"verified":false,"klipper_left_stopped":false,
			"backup_manifest":"` + backup + `"
		}`),
	} {
		t.Run(name, func(t *testing.T) {
			schema := PLRDeploymentSchema
			if strings.Contains(name, "rollback") {
				schema = PLRRollbackSchema
			}
			candidate, err := ParsePLROperation(document, schema)
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidatePLRLifecycle(candidate); err == nil {
				t.Fatal("expected state-specific handle rejection")
			}
		})
	}

	_, err = ParsePLROperation([]byte(`{
		"schema":"qidi-plus4-plr-deployment/v1",
		"backup_manifest":"one","verification":{"backup_manifest":"two"}
	}`), PLRDeploymentSchema)
	if err == nil {
		t.Fatal("expected nested handle conflict rejection")
	}
}
