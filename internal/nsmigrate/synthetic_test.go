package nsmigrate

import (
	"os"
	"path/filepath"
	"testing"
)

func TestGenerateSyntheticDBSupportsDryRunApplyAndInverse(t *testing.T) {
	manifest := parseManifestText(t, `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,worker,personal.worker,migrate,example/personal,synthetic active worker,false
jk,dual-worker,personal.dual-worker,split,example/shared,synthetic JK split,false
ucla,dual-worker,ucla.dual-worker,split,example/shared,synthetic UCLA split,false
jk,personal.existing,personal.existing,unchanged,example/personal,synthetic existing namespace,false
jk,managerd,managerd,unchanged,example/manager,synthetic control plane,true
`)
	path := filepath.Join(t.TempDir(), "synthetic.db")
	options := SyntheticOptions{
		DBPath:             path,
		Authority:          AuthorityJK,
		ControlPlaneAgents: []string{"managerd"},
	}
	report, err := GenerateSyntheticDB(t.Context(), manifest, options)
	if err != nil {
		t.Fatalf("generate synthetic database: %v", err)
	}
	if report.Identities != 4 || report.Conversations != 4 || report.Messages != 4 {
		t.Fatalf("synthetic report = %#v", report)
	}
	before := snapshotDB(t, path)

	dryRun, err := Run(t.Context(), manifest, Options{
		DBPath:             path,
		Authority:          AuthorityJK,
		AcknowledgeCopy:    true,
		ControlPlaneAgents: []string{"managerd"},
	})
	if err != nil {
		t.Fatalf("dry-run synthetic database: %v", err)
	}
	if dryRun.Status != "ready" {
		t.Fatalf("dry-run status = %q", dryRun.Status)
	}
	for _, surface := range surfaces {
		if dryRun.RewritesBySurface[surface] == 0 {
			t.Fatalf("surface %q has no synthetic rewrite coverage: %#v", surface, dryRun.RewritesBySurface)
		}
	}

	apply, err := Run(t.Context(), manifest, Options{
		DBPath:             path,
		Authority:          AuthorityJK,
		Apply:              true,
		AcknowledgeCopy:    true,
		ControlPlaneAgents: []string{"managerd"},
	})
	if err != nil || apply.Status != "applied" {
		t.Fatalf("apply status=%q err=%v", apply.Status, err)
	}

	inverse := inverseManifest(t, manifest)
	undo, err := Run(t.Context(), inverse, Options{
		DBPath:             path,
		Authority:          AuthorityJK,
		Apply:              true,
		AcknowledgeCopy:    true,
		ControlPlaneAgents: []string{"managerd"},
	})
	if err != nil || undo.Status != "applied" {
		t.Fatalf("inverse status=%q err=%v", undo.Status, err)
	}
	if after := snapshotDB(t, path); after != before {
		t.Fatal("inverse did not restore the synthetic database exactly")
	}
}

func TestGenerateSyntheticDBRefusesExistingDestination(t *testing.T) {
	manifest := loadManifestFixture(t, "success_manifest.csv")
	path := filepath.Join(t.TempDir(), "existing.db")
	if err := os.WriteFile(path, []byte("do not overwrite"), 0o600); err != nil {
		t.Fatalf("write existing destination: %v", err)
	}
	if _, err := GenerateSyntheticDB(t.Context(), manifest, SyntheticOptions{
		DBPath:    path,
		Authority: AuthorityJK,
	}); err == nil {
		t.Fatal("expected existing destination refusal")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read existing destination: %v", err)
	}
	if string(contents) != "do not overwrite" {
		t.Fatalf("existing destination changed: %q", contents)
	}
}
