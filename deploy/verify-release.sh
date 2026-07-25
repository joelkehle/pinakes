#!/usr/bin/env bash
set -euo pipefail

die() {
  printf 'error: %s\n' "$*" >&2
  exit 1
}

if [[ $# -ne 1 || ! "$1" =~ ^v[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
  printf 'usage: %s <vMAJOR.MINOR.PATCH>\n' "$0" >&2
  exit 2
fi

for command in docker git go mktemp trash; do
  command -v "$command" >/dev/null || die "missing required command: $command"
done

tag="$1"
image="ghcr.io/joelkehle/pinakes:$tag"
repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
artifact_dir=""

cleanup() {
  local status=$?
  trap - EXIT
  if [[ -n "$artifact_dir" && -d "$artifact_dir" ]]; then
    trash "$artifact_dir" >/dev/null 2>&1 || true
  fi
  exit "$status"
}
trap cleanup EXIT

local_commit="$(git -C "$repo_root" rev-list -n 1 "$tag")"
[[ -n "$local_commit" ]] || die "local tag does not resolve to a commit: $tag"

remote_tag="$(
  git -C "$repo_root" ls-remote --tags origin "refs/tags/$tag" |
    awk 'NR == 1 {print $1}'
)"
[[ -n "$remote_tag" ]] || die "remote tag does not exist: $tag"

remote_peeled="$(
  git -C "$repo_root" ls-remote --tags origin "refs/tags/$tag^{}" |
    awk 'NR == 1 {print $1}'
)"
remote_commit="${remote_peeled:-$remote_tag}"
[[ "$remote_commit" == "$local_commit" ]] ||
  die "remote tag commit $remote_commit does not match local $local_commit"

docker manifest inspect "$image" >/dev/null
docker pull "$image" >/dev/null

artifact_dir="$(mktemp -d)"
docker run --rm --entrypoint /bin/sh \
  -v "$artifact_dir:/out" \
  "$image" \
  -c 'cp /usr/local/bin/pinakes /out/pinakes'

build_info="$(go version -m "$artifact_dir/pinakes")"
artifact_revision="$(
  awk -F= '$1 ~ /vcs.revision$/ {print $2}' <<<"$build_info"
)"
artifact_modified="$(
  awk -F= '$1 ~ /vcs.modified$/ {print $2}' <<<"$build_info"
)"
artifact_version="$(
  awk '$1 == "mod" && $2 == "github.com/joelkehle/pinakes" {print $3}' \
    <<<"$build_info"
)"

[[ "$artifact_revision" == "$local_commit" ]] ||
  die "artifact revision ${artifact_revision:-missing} does not match $local_commit"
[[ "$artifact_modified" == "false" ]] ||
  die "artifact build is modified or lacks clean VCS metadata"
[[ "$artifact_version" == "$tag" ]] ||
  die "artifact module version ${artifact_version:-missing} does not match $tag"

digest="$(
  docker image inspect "$image" --format '{{index .RepoDigests 0}}'
)"
[[ -n "$digest" ]] || die "pulled image has no repository digest"

printf 'release_tag=%s\n' "$tag"
printf 'release_commit=%s\n' "$local_commit"
printf 'image_digest=%s\n' "$digest"
printf 'artifact_vcs_modified=%s\n' "$artifact_modified"
