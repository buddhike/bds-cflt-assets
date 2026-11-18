#!/usr/bin/env bash
#
# Finds the projects that have a release to publish.
#
# A project is a directory below the repository root with a release.md. The
# file holds only the project's version, e.g. "0.1.0" or "0.1.0-rc.1". A
# project has a release to publish when the git tag "<project>-v<version>"
# does not exist yet, where <project> is the directory name. That makes the
# workflow safe to re-run: a failed release is retried, and a version that was
# already released is never released twice.
#
# Run from the repository root. Writes to $GITHUB_OUTPUT:
#   wails  JSON array of releasable Wails projects (those with a wails.json)
# and a summary table to $GITHUB_STEP_SUMMARY.

set -euo pipefail

out=${GITHUB_OUTPUT:-/dev/stdout}
summary=${GITHUB_STEP_SUMMARY:-/dev/null}
remote=${RELEASE_REMOTE:-origin}

# Strict on purpose: these values end up in tag names, file names and shell.
name_re='^[A-Za-z0-9][A-Za-z0-9._-]*$'
version_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

wails='[]'
seen=$'\n'
failed=0

{
  echo '### Releases'
  echo
  echo '| Project | Version | Result |'
  echo '|---|---|---|'
} >> "$summary"

report() { echo "| $1 | $2 | $3 |" >> "$summary"; }

# Prints an error annotation and fails the run once every project was checked.
fail() {
  echo "::error file=$1::$2"
  failed=1
}

while IFS= read -r file; do
  file=${file#./}
  dir=$(dirname "$file")
  name=$(basename "$dir")

  if ! [[ $name =~ $name_re ]]; then
    fail "$file" "Project directory name '$name' must match $name_re."
    report "$dir" '?' 'invalid directory name'
    continue
  fi

  case $seen in
    *$'\n'"$name"$'\n'*)
      fail "$file" "Another project is already called '$name'. Release tags use the directory name, so it must be unique."
      report "$dir" '?' 'duplicate project name'
      continue
      ;;
  esac
  seen+="$name"$'\n'

  # The version is the whole file, trimmed, with an optional leading "v".
  version=$(tr -d '\r' < "$file" | sed -e 's/^[[:space:]]*//' -e 's/[[:space:]]*$//')
  version=${version#v}
  if ! [[ $version =~ $version_re ]]; then
    fail "$file" "release.md must contain only a version such as 1.2.3 or 1.2.3-rc.1, found: $(printf '%q' "$version")"
    report "$dir" '?' 'invalid release.md'
    continue
  fi

  tag="$name-v$version"

  # ls-remote exits 0 when the tag exists, 2 when it does not, and anything
  # else when the remote could not be queried. Only 2 means "not released".
  rc=0
  git ls-remote --exit-code --tags "$remote" "refs/tags/$tag" > /dev/null 2>&1 || rc=$?
  case $rc in
    0)
      report "$dir" "$version" "already released as \`$tag\`"
      continue
      ;;
    2) ;;
    *)
      fail "$file" "Could not check whether tag $tag exists (git ls-remote exited $rc)."
      report "$dir" "$version" 'could not query tags'
      continue
      ;;
  esac

  if [ -f "$dir/wails.json" ]; then
    prerelease=false
    [[ $version == *-* ]] && prerelease=true
    wails=$(jq -c \
      --arg name "$name" --arg dir "$dir" --arg version "$version" \
      --arg tag "$tag" --argjson prerelease "$prerelease" \
      '. + [{name: $name, dir: $dir, version: $version, tag: $tag, prerelease: $prerelease}]' \
      <<< "$wails")
    report "$dir" "$version" "will release \`$tag\` (Wails)"
  else
    echo "::warning file=$file::$dir has no wails.json, and Wails is the only project type with a build recipe, so nothing was built for $tag."
    report "$dir" "$version" 'skipped: no build recipe for this project type'
  fi
done < <(find . -mindepth 2 -name release.md -not -path './.git/*' -not -path '*/node_modules/*' | sort)

echo "wails=$wails" >> "$out"

exit "$failed"
