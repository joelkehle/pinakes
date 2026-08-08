package nsmigrate

import (
	"strings"
	"testing"
)

func TestManifestRequiresBothSplitAuthorities(t *testing.T) {
	input := `source_authority,source_id,target_id,disposition,owner_repo,evidence
jk,dual-canary,personal.dual-canary,split,example/repo,synthetic
`
	_, err := ParseManifest(strings.NewReader(input))
	if code := refusalCode(t, err); code != "manifest_schema" {
		t.Fatalf("refusal code = %q, want manifest_schema", code)
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
