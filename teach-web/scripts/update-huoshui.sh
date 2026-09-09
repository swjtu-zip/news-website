#!/usr/bin/env bash
# Sync Huoshui course-rating JSON data from swjtu-zip/huoshui-scraper
# (branch master, docs/data/) into teach-web/data/huoshui/.
#
# Usage: teach-web/scripts/update-huoshui.sh
# Can be run manually or by .github/workflows/huoshui-sync.yml.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DATA_DIR="$(cd "$SCRIPT_DIR/.." && pwd)/data/huoshui"
BASE_URL="https://raw.githubusercontent.com/swjtu-zip/huoshui-scraper/master/docs/data"
COMMITS_API="https://api.github.com/repos/swjtu-zip/huoshui-scraper/commits?path=docs/data&per_page=1"
FILES=(courses.json reviews.json stats.json)

# huoshui-scraper is a private repo: raw.githubusercontent.com requires a
# token. Use HUOSHUI_GITHUB_TOKEN / GH_TOKEN / GITHUB_TOKEN if set, otherwise
# fall back to `gh auth token` when the gh CLI is available.
TOKEN="${HUOSHUI_GITHUB_TOKEN:-${GH_TOKEN:-${GITHUB_TOKEN:-}}}"
if [ -z "$TOKEN" ] && command -v gh > /dev/null 2>&1; then
  TOKEN="$(gh auth token 2>/dev/null)" || TOKEN=""
fi
AUTH_HEADER=()
if [ -n "$TOKEN" ]; then
  AUTH_HEADER=(-H "Authorization: Bearer $TOKEN")
else
  echo "==> warning: no GitHub token found; anonymous access will 404 for private repos" >&2
fi

mkdir -p "$DATA_DIR"

fetch_file() {
  local name="$1"
  local target="$DATA_DIR/$name"
  local tmp
  tmp="$(mktemp)"
  trap 'rm -f "$tmp"' RETURN

  echo "==> $name"
  curl -fsSL --retry 3 "${AUTH_HEADER[@]}" -o "$tmp" "$BASE_URL/$name"

  # Refuse to replace the local copy with invalid JSON.
  if ! python3 -m json.tool "$tmp" > /dev/null; then
    echo "    error: downloaded $name is not valid JSON, keeping local copy" >&2
    return 1
  fi

  if [ -f "$target" ] && [ "$(sha256sum "$tmp" | cut -d' ' -f1)" = "$(sha256sum "$target" | cut -d' ' -f1)" ]; then
    echo "    unchanged"
    return 0
  fi

  chmod 644 "$tmp"
  mv -f "$tmp" "$target"
  echo "    updated"
}

for name in "${FILES[@]}"; do
  fetch_file "$name"
done

# Query the latest upstream commit that touched docs/data. A failure here
# (e.g. GitHub API rate limit) must not abort the sync: commit fields stay null.
upstream_sha="null"
upstream_date="null"
commit_json="$(curl -fsSL --retry 2 \
  "${AUTH_HEADER[@]}" \
  -H "Accept: application/vnd.github+json" \
  -H "User-Agent: teach-web-huoshui-sync" \
  "$COMMITS_API" 2>/dev/null)" || commit_json=""
if [ -n "$commit_json" ]; then
  commit_fields="$(printf '%s' "$commit_json" | python3 -c "
import json, sys
try:
    data = json.load(sys.stdin)
    item = data[0]
    print(item['sha'])
    print(item['commit']['committer']['date'])
except Exception:
    pass
" 2>/dev/null)" || commit_fields=""
  if [ -n "$commit_fields" ]; then
    upstream_sha="$(printf '%s\n' "$commit_fields" | sed -n '1p')"
    upstream_date="$(printf '%s\n' "$commit_fields" | sed -n '2p')"
    [ -n "$upstream_sha" ] || upstream_sha="null"
    [ -n "$upstream_date" ] || upstream_date="null"
  fi
fi
if [ "$upstream_sha" = "null" ]; then
  echo "==> warning: could not determine upstream commit (rate limit?), meta.json will use null" >&2
fi

python3 - "$DATA_DIR" "$upstream_sha" "$upstream_date" <<'PY'
import hashlib
import json
import sys
from datetime import datetime, timezone

data_dir, sha, date = sys.argv[1], sys.argv[2], sys.argv[3]
files = {}
for name in ("courses.json", "reviews.json", "stats.json"):
    path = f"{data_dir}/{name}"
    with open(path, "rb") as fh:
        content = fh.read()
    files[name] = {"bytes": len(content), "sha256": hashlib.sha256(content).hexdigest()}

meta = {
    "fetchedAt": datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ"),
    "upstreamCommit": None if sha == "null" else sha,
    "upstreamCommittedAt": None if date == "null" else date,
    "files": files,
}
with open(f"{data_dir}/meta.json", "w", encoding="utf-8") as fh:
    json.dump(meta, fh, ensure_ascii=False, indent=2)
    fh.write("\n")
print(f"==> meta.json written (upstream commit: {sha})")
PY

echo "==> done"
