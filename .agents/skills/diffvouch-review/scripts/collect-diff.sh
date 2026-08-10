#!/usr/bin/env bash

set -euo pipefail

mode="${1:-working-tree}"
base_ref="${2:-}"

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
skipped_symlinks=()
if [[ "$include_untracked" == true ]]; then
  while IFS= read -r -d '' path; do
    if [[ -L "$path" ]]; then
      skipped_symlinks+=("$path")
    else
      untracked_files+=("$path")
    fi
  done < <(git ls-files --others --exclude-standard -z)
fi

printf 'DIFFVOUCH_REVIEW_CONTEXT_V1\n'
printf 'repository=%s\n' "$repo_root"
printf 'scope=%s\n' "$scope_label"
printf 'head=%s\n' "${head_sha:-unborn}"
printf 'merge_base=%s\n' "${merge_base:-none}"
printf 'untracked_files=%d\n' "${#untracked_files[@]}"
printf 'skipped_untracked_symlinks=%d\n' "${#skipped_symlinks[@]}"

for path in "${skipped_symlinks[@]}"; do
  printf 'skipped_symlink=%q\n' "$path"
done

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
