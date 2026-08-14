#!/usr/bin/env bash

set -euo pipefail

mode="${1:-working-tree}"
base_ref="${2:-}"
max_diff_bytes="${DIFFVOUCH_MAX_DIFF_BYTES:-500000}"

if [[ ! "$max_diff_bytes" =~ ^[1-9][0-9]*$ ]]; then
  echo "error: DIFFVOUCH_MAX_DIFF_BYTES must be a positive integer" >&2
  exit 2
fi

repo_root="$(git rev-parse --show-toplevel 2>/dev/null)" || {
  echo "error: not inside a Git repository" >&2
  exit 3
}
cd "$repo_root"

head_sha="$(git rev-parse --verify HEAD 2>/dev/null || true)"
merge_base=""
include_untracked=false
scope_label=""
diff_args=()

case "$mode" in
  working-tree)
    include_untracked=true
    scope_label="working-tree"
    if [[ -n "$head_sha" ]]; then
      diff_args=(diff --find-renames --no-ext-diff --no-textconv "$head_sha" --)
    else
      empty_tree="$(git hash-object -t tree /dev/null)"
      diff_args=(diff --find-renames --no-ext-diff --no-textconv "$empty_tree" --)
    fi
    ;;
  staged)
    scope_label="staged"
    diff_args=(diff --cached --find-renames --no-ext-diff --no-textconv --)
    ;;
  base|committed)
    if [[ -z "$base_ref" ]]; then
      echo "error: mode '$mode' requires a base ref" >&2
      exit 2
    fi
    if [[ -z "$head_sha" ]]; then
      echo "error: mode '$mode' requires an existing HEAD commit" >&2
      exit 3
    fi
    base_commit="$(git rev-parse --verify "${base_ref}^{commit}" 2>/dev/null)" || {
      echo "error: base ref '$base_ref' does not resolve to a commit" >&2
      exit 3
    }
    merge_base="$(git merge-base "$head_sha" "$base_commit" 2>/dev/null)" || {
      echo "error: HEAD and '$base_ref' do not have a merge base" >&2
      exit 3
    }
    if [[ "$mode" == "base" ]]; then
      include_untracked=true
      scope_label="merge-base(${base_ref})..working-tree"
      diff_args=(diff --find-renames --no-ext-diff --no-textconv "$merge_base" --)
    else
      scope_label="merge-base(${base_ref})..HEAD"
      diff_args=(diff --find-renames --no-ext-diff --no-textconv "$merge_base" "$head_sha" --)
    fi
    ;;
  *)
    echo "error: unsupported mode '$mode'; use working-tree, staged, base, or committed" >&2
    exit 2
    ;;
esac

untracked_files=()
untracked_symlinks=0
if [[ "$include_untracked" == true ]]; then
  while IFS= read -r -d '' path; do
    if [[ -L "$path" ]]; then
      ((untracked_symlinks += 1))
    fi
    untracked_files+=("$path")
  done < <(git ls-files --others --exclude-standard -z)
fi

patch_file="$(mktemp "${TMPDIR:-/tmp}/diffvouch-patch.XXXXXX")" || {
  echo "error: could not create a temporary patch file" >&2
  exit 3
}
trap 'rm -f -- "$patch_file"' EXIT

{
  printf '%s\n' '--- BEGIN TRACKED PATCH ---'
  git "${diff_args[@]}"
  printf '%s\n' '--- END TRACKED PATCH ---'

  for path in "${untracked_files[@]}"; do
    printf '%s\n' "--- BEGIN UNTRACKED FILE: $path ---"
    status=0
    git diff --no-index --no-ext-diff --no-textconv -- /dev/null "$path" || status=$?
    if (( status > 1 )); then
      echo "error: failed to create patch for untracked file '$path'" >&2
      exit 3
    fi
    printf '%s\n' "--- END UNTRACKED FILE: $path ---"
  done
} >"$patch_file"

patch_bytes="$(wc -c <"$patch_file")"
patch_bytes="${patch_bytes//[[:space:]]/}"
if (( patch_bytes > max_diff_bytes )); then
  echo "error: collected patch is ${patch_bytes} bytes; limit is ${max_diff_bytes} bytes" >&2
  echo "error: review coverage cannot be guaranteed; no patch was emitted and publication is forbidden" >&2
  exit 6
fi

printf 'DIFFVOUCH_REVIEW_CONTEXT_V1\n'
printf 'repository=%s\n' "$repo_root"
printf 'scope=%s\n' "$scope_label"
printf 'head=%s\n' "${head_sha:-unborn}"
printf 'merge_base=%s\n' "${merge_base:-none}"
printf 'patch_bytes=%s\n' "$patch_bytes"
printf 'max_diff_bytes=%s\n' "$max_diff_bytes"
printf 'partial=false\n'
printf 'untracked_files=%d\n' "${#untracked_files[@]}"
printf 'untracked_symlinks=%d\n' "$untracked_symlinks"

cat "$patch_file"
printf 'DIFFVOUCH_REVIEW_CONTEXT_END_V1 patch_bytes=%s\n' "$patch_bytes"
