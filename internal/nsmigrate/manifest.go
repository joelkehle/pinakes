package nsmigrate

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode"
	"unicode/utf8"
)

type Authority string

const (
	AuthorityJK   Authority = "jk"
	AuthorityUCLA Authority = "ucla"
)

type Disposition string

const (
	DispositionMigrate   Disposition = "migrate"
	DispositionRetire    Disposition = "retire"
	DispositionSplit     Disposition = "split"
	DispositionUnchanged Disposition = "unchanged"
)

var manifestHeader = []string{
	"source_authority",
	"source_id",
	"target_id",
	"disposition",
	"owner_repo",
	"evidence",
}

type ManifestRow struct {
	SourceAuthority Authority
	SourceID        string
	TargetID        string
	Disposition     Disposition
	OwnerRepo       string
	Evidence        string
	Line            int
}

type Manifest struct {
	Rows []ManifestRow
}

type Refusal struct {
	Code     string `json:"code"`
	Location string `json:"location,omitempty"`
	Detail   string `json:"detail"`
}

type RefusalError struct {
	Refusals []Refusal
}

func (e *RefusalError) Error() string {
	if len(e.Refusals) == 0 {
		return "migration refused"
	}
	return fmt.Sprintf("migration refused: %s", e.Refusals[0].Detail)
}

func ParseAuthority(raw string) (Authority, error) {
	authority := Authority(raw)
	switch authority {
	case AuthorityJK, AuthorityUCLA:
		return authority, nil
	default:
		return "", fmt.Errorf("authority must be jk or ucla, got %q", raw)
	}
}

func ParseManifest(r io.Reader) (Manifest, error) {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	reader.ReuseRecord = false

	header, err := reader.Read()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return Manifest{}, refuse("manifest_schema", "manifest", "manifest is empty")
		}
		return Manifest{}, refuse("manifest_schema", "manifest", "read CSV header: "+err.Error())
	}
	if err := validateHeader(header); err != nil {
		return Manifest{}, err
	}

	manifest := Manifest{}
	seenSources := map[string]int{}
	targetOwners := map[string]ManifestRow{}
	for line := 2; ; line++ {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return Manifest{}, refuse("manifest_schema", fmt.Sprintf("manifest:%d", line), "read CSV row: "+err.Error())
		}
		if len(record) != len(manifestHeader) {
			return Manifest{}, refuse(
				"manifest_schema",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("expected %d fields, got %d", len(manifestHeader), len(record)),
			)
		}
		row, err := parseManifestRow(record, line)
		if err != nil {
			return Manifest{}, err
		}

		sourceKey := string(row.SourceAuthority) + "\x00" + row.SourceID
		if previous, ok := seenSources[sourceKey]; ok {
			return Manifest{}, refuse(
				"duplicate_source",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("duplicate (source_authority, source_id); first declared on line %d", previous),
			)
		}
		seenSources[sourceKey] = line

		// Unchanged rows also reserve their target: that identity already exists, so
		// a rename from another source must not be allowed to collide with it.
		targetKey := string(row.SourceAuthority) + "\x00" + row.TargetID
		if previous, ok := targetOwners[targetKey]; ok && previous.SourceID != row.SourceID {
			return Manifest{}, refuse(
				"duplicate_target",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("target_id is already mapped from a different source on line %d", previous.Line),
			)
		}
		targetOwners[targetKey] = row
		manifest.Rows = append(manifest.Rows, row)
	}
	if len(manifest.Rows) == 0 {
		return Manifest{}, refuse("manifest_schema", "manifest", "manifest has no data rows")
	}
	if err := validateSplitPairs(manifest.Rows); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

func validateHeader(header []string) error {
	if len(header) > len(manifestHeader) {
		return refuse(
			"outside_rewrite_surface",
			"manifest:1",
			"manifest contains columns outside the six-column contract",
		)
	}
	if len(header) != len(manifestHeader) {
		return refuse(
			"manifest_schema",
			"manifest:1",
			fmt.Sprintf("header must contain exactly %d columns", len(manifestHeader)),
		)
	}
	for i := range manifestHeader {
		if header[i] != manifestHeader[i] {
			return refuse(
				"manifest_schema",
				"manifest:1",
				fmt.Sprintf("column %d must be %q", i+1, manifestHeader[i]),
			)
		}
	}
	return nil
}

func parseManifestRow(record []string, line int) (ManifestRow, error) {
	for i, value := range record {
		if !utf8.ValidString(value) {
			return ManifestRow{}, refuse("manifest_schema", fmt.Sprintf("manifest:%d", line), "field is not valid UTF-8")
		}
		if strings.TrimSpace(value) != value {
			return ManifestRow{}, refuse(
				"manifest_schema",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("%s must not have surrounding whitespace", manifestHeader[i]),
			)
		}
		if value == "" {
			return ManifestRow{}, refuse(
				"manifest_schema",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("%s must be non-empty", manifestHeader[i]),
			)
		}
	}
	for _, value := range record[:3] {
		if strings.IndexFunc(value, unicode.IsControl) >= 0 {
			return ManifestRow{}, refuse("manifest_schema", fmt.Sprintf("manifest:%d", line), "identity fields must not contain control characters")
		}
	}

	authority, err := ParseAuthority(record[0])
	if err != nil {
		return ManifestRow{}, refuse("manifest_schema", fmt.Sprintf("manifest:%d", line), err.Error())
	}
	disposition := Disposition(record[3])
	switch disposition {
	case DispositionMigrate, DispositionRetire, DispositionSplit, DispositionUnchanged:
	default:
		return ManifestRow{}, refuse(
			"invalid_disposition",
			fmt.Sprintf("manifest:%d", line),
			"disposition must be migrate, retire, split, or unchanged",
		)
	}

	row := ManifestRow{
		SourceAuthority: authority,
		SourceID:        record[1],
		TargetID:        record[2],
		Disposition:     disposition,
		OwnerRepo:       record[4],
		Evidence:        record[5],
		Line:            line,
	}
	if err := validateMapping(row); err != nil {
		return ManifestRow{}, err
	}
	return row, nil
}

func validateMapping(row ManifestRow) error {
	location := fmt.Sprintf("manifest:%d", row.Line)
	if foreignAuthorityScope(row.SourceAuthority, row.SourceID) ||
		foreignAuthorityScope(row.SourceAuthority, row.TargetID) {
		if row.Disposition != DispositionUnchanged || row.SourceID != row.TargetID {
			return refuse(
				"foreign_authority_mutation",
				location,
				"foreign-authority identities may only be declared unchanged with target_id equal to source_id",
			)
		}
	}
	if row.Disposition == DispositionUnchanged {
		if row.SourceID != row.TargetID {
			return refuse("manifest_schema", location, "unchanged requires source_id == target_id")
		}
		if _, ok := explicitScope(row.SourceID); !ok {
			return refuse("manifest_schema", location, "unchanged identity must already have an explicit namespace")
		}
		return nil
	}
	if row.SourceID == row.TargetID {
		return refuse("manifest_schema", location, "rename dispositions require source_id != target_id")
	}

	prefix := "ucla."
	if row.SourceAuthority == AuthorityJK {
		prefix = "personal."
	}
	forward := !hasExplicitScope(row.SourceID) && row.TargetID == prefix+row.SourceID
	inverse := !hasExplicitScope(row.TargetID) && row.SourceID == prefix+row.TargetID
	if !forward && !inverse {
		return refuse(
			"manifest_schema",
			location,
			fmt.Sprintf("mapping must preserve the complete legacy ID with the %q authority prefix, in either direction", prefix),
		)
	}
	return nil
}

func validateSplitPairs(rows []ManifestRow) error {
	type pair struct {
		jkLine   int
		uclaLine int
	}
	pairs := map[string]pair{}
	for _, row := range rows {
		if row.Disposition != DispositionSplit {
			continue
		}
		legacyID := row.SourceID
		if hasExplicitScope(row.SourceID) {
			legacyID = row.TargetID
		}
		current := pairs[legacyID]
		switch row.SourceAuthority {
		case AuthorityJK:
			current.jkLine = row.Line
		case AuthorityUCLA:
			current.uclaLine = row.Line
		}
		pairs[legacyID] = current
	}
	for legacyID, current := range pairs {
		if current.jkLine == 0 || current.uclaLine == 0 {
			line := current.jkLine
			if line == 0 {
				line = current.uclaLine
			}
			return refuse(
				"manifest_schema",
				fmt.Sprintf("manifest:%d", line),
				fmt.Sprintf("split identity %q requires one jk row and one ucla row", legacyID),
			)
		}
	}
	return nil
}

func explicitScope(id string) (string, bool) {
	prefix, _, ok := strings.Cut(id, ".")
	if !ok {
		return "", false
	}
	switch prefix {
	case "personal", "ucla", "shared":
		return prefix, true
	default:
		return "", false
	}
}

func hasExplicitScope(id string) bool {
	_, ok := explicitScope(id)
	return ok
}

func foreignAuthorityScope(authority Authority, id string) bool {
	scope, ok := explicitScope(id)
	if !ok {
		return false
	}
	return authority == AuthorityJK && scope == "ucla" ||
		authority == AuthorityUCLA && scope == "personal"
}

func refuse(code, location, detail string) error {
	return &RefusalError{Refusals: []Refusal{{
		Code:     code,
		Location: location,
		Detail:   detail,
	}}}
}
