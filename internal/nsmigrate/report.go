package nsmigrate

const ReportVersion = 1

var dispositions = []Disposition{
	DispositionMigrate,
	DispositionRetire,
	DispositionSplit,
	DispositionUnchanged,
}

var surfaces = []string{
	"agents.agent_id",
	"messages.from_agent",
	"messages.to_agent",
	"conversations.participants",
	"deliveries.target_agent_id",
	"delivery_cursors.target_agent_id",
	"idempotency.from_agent",
	"idempotency.to_agent",
}

type Report struct {
	Version                int            `json:"version"`
	Mode                   string         `json:"mode"`
	Authority              Authority      `json:"authority"`
	Status                 string         `json:"status"`
	ManifestRows           int            `json:"manifest_rows"`
	ManifestByDisposition  map[string]int `json:"manifest_by_disposition"`
	PlannedByDisposition   map[string]int `json:"planned_by_disposition"`
	RewritesBySurface      map[string]int `json:"rewrites_by_surface"`
	TotalRewrites          int            `json:"total_rewrites"`
	AlreadyAppliedMappings int            `json:"already_applied_mappings"`
	Collisions             []Collision    `json:"collisions"`
	Refusals               []Refusal      `json:"refusals"`
}

type Collision struct {
	Code         string `json:"code"`
	SourceID     string `json:"source_id"`
	TargetID     string `json:"target_id"`
	ManifestLine int    `json:"manifest_line"`
}

func newReport(options Options) Report {
	mode := "dry-run"
	if options.Apply {
		mode = "apply"
	}
	report := Report{
		Version:               ReportVersion,
		Mode:                  mode,
		Authority:             options.Authority,
		Status:                "refused",
		ManifestByDisposition: map[string]int{},
		PlannedByDisposition:  map[string]int{},
		RewritesBySurface:     map[string]int{},
		Collisions:            []Collision{},
		Refusals:              []Refusal{},
	}
	for _, disposition := range dispositions {
		report.ManifestByDisposition[string(disposition)] = 0
		report.PlannedByDisposition[string(disposition)] = 0
	}
	for _, surface := range surfaces {
		report.RewritesBySurface[surface] = 0
	}
	return report
}

func RefusedReport(authority Authority, apply bool, refusals []Refusal) Report {
	report := newReport(Options{Authority: authority, Apply: apply})
	report.Refusals = append(report.Refusals, refusals...)
	return report
}
