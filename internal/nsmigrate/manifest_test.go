package nsmigrate

import (
	"strings"
	"testing"
)

func TestManifestRequiresBothSplitAuthorities(t *testing.T) {
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,dual-canary,personal.dual-canary,split,example/repo,synthetic,false
`
	_, err := ParseManifest(strings.NewReader(input))
	if code := refusalCode(t, err); code != "manifest_schema" {
		t.Fatalf("refusal code = %q, want manifest_schema", code)
	}
}

func TestControlPlaneManifestMarker(t *testing.T) {
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,managerd,managerd,unchanged,example/manager,synthetic control plane,true
`
	manifest, err := ParseManifest(strings.NewReader(input))
	if err != nil {
		t.Fatalf("parse control-plane manifest: %v", err)
	}
	if len(manifest.Rows) != 1 || !manifest.Rows[0].ControlPlane {
		t.Fatalf("control-plane row = %#v", manifest.Rows)
	}
}

func TestUnmarkedUnprefixedUnchangedStillRefused(t *testing.T) {
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane
jk,managerd,managerd,unchanged,example/manager,synthetic control plane,false
`
	_, err := ParseManifest(strings.NewReader(input))
	if code := refusalCode(t, err); code != "manifest_schema" {
		t.Fatalf("refusal code = %q, want manifest_schema", code)
	}
}

func TestControlPlaneMarkerRequiresUnprefixedUnchangedRow(t *testing.T) {
	cases := []string{
		"jk,managerd,personal.managerd,migrate,example/manager,synthetic,true",
		"jk,personal.managerd,personal.managerd,unchanged,example/manager,synthetic,true",
		"jk,ops.managerd,ops.managerd,unchanged,example/manager,synthetic,true",
	}
	for _, row := range cases {
		input := "source_authority,source_id,target_id,disposition,owner_repo,evidence,control_plane\n" + row + "\n"
		_, err := ParseManifest(strings.NewReader(input))
		if code := refusalCode(t, err); code != "manifest_schema" {
			t.Fatalf("row %q refusal code = %q, want manifest_schema", row, code)
		}
	}
}

func TestInverseManifestPassesSchema(t *testing.T) {
	manifest := loadManifestFixture(t, "success_manifest.csv")
	inverse := inverseManifest(t, manifest)
	if len(inverse.Rows) != len(manifest.Rows) {
		t.Fatalf("inverse rows = %d, want %d", len(inverse.Rows), len(manifest.Rows))
	}
	for index := range manifest.Rows {
		if inverse.Rows[index].SourceID != manifest.Rows[index].TargetID ||
			inverse.Rows[index].TargetID != manifest.Rows[index].SourceID {
			t.Fatalf("inverse row %d = %#v", index, inverse.Rows[index])
		}
	}
}
