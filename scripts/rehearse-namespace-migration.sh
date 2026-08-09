#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: CONTROL_PLANE_AGENTS=... $0 COMBINED_MANIFEST.csv NEW_OUTPUT_DIR" >&2
  exit 2
fi

manifest=$1
output_dir=$2

if [[ ! -f "$manifest" ]]; then
  echo "manifest does not exist: $manifest" >&2
  exit 2
fi
if [[ -e "$output_dir" ]]; then
  echo "output directory already exists: $output_dir" >&2
  exit 2
fi

mkdir -m 700 "$output_dir"

go run ./cmd/pinakes-manifest-invert \
  --manifest "$manifest" \
  --output "$output_dir/inverse-manifest.csv"

for authority in jk ucla; do
  pristine="$output_dir/$authority-pristine-synthetic.db"
  working="$output_dir/$authority-rewrite-working.db"
  inverse_working="$output_dir/$authority-inverse-working.db"

  go run ./cmd/pinakes-migrate-fixture \
    --db "$pristine" \
    --manifest "$manifest" \
    --authority "$authority" \
    >"$output_dir/$authority-fixture.json"

  cp "$pristine" "$working"
  go run ./cmd/pinakes-migrate \
    --db "$working" \
    --manifest "$manifest" \
    --authority "$authority" \
    --acknowledge-copy \
    >"$output_dir/$authority-dry-run.json"
  go run ./cmd/pinakes-migrate \
    --db "$working" \
    --manifest "$manifest" \
    --authority "$authority" \
    --acknowledge-copy \
    --apply \
    >"$output_dir/$authority-apply.json"

  cp "$working" "$inverse_working"
  go run ./cmd/pinakes-migrate \
    --db "$inverse_working" \
    --manifest "$output_dir/inverse-manifest.csv" \
    --authority "$authority" \
    --acknowledge-copy \
    --apply \
    >"$output_dir/$authority-inverse-apply.json"

  go run ./cmd/pinakes-migrate \
    --db "$inverse_working" \
    --manifest "$manifest" \
    --authority "$authority" \
    --acknowledge-copy \
    >"$output_dir/$authority-restored-dry-run.json"
done

go run ./cmd/pinakes-empty-strict-rehearsal \
  --db "$output_dir/empty-strict-rehearsal.db" \
  --allowlist rehearsal/issue15/post-migration-allowlist.txt \
  --agents personal.jk-gmail-ingest,ucla.ucla-tdg-gmail-ingest,personal.jk-calendar-guard-agent,ucla.jk-calendar-guard-agent \
  >"$output_dir/empty-strict-rehearsal.json"

echo "synthetic rehearsal evidence: $output_dir"
