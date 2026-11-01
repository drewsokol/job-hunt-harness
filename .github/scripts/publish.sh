#!/usr/bin/env bash
# Publishes what the build job left (decision 0011): pushes the branch,
# posts the reviews and the final message, writes the PR description and
# sets the labels. Run by the publish job in .github/workflows/build.yml,
# with a token that can write.
#
# Everything in $OUT came from the build job, which ran Claude and the code
# Claude wrote, so it's checked here before any of it reaches GitHub: the
# result's fields must have the right shape, the branch mustn't change the
# workflows, specs, privacy scanner or old decision records, and the
# commits and every text pass the privacy scan's general rules. This script
# runs no code from the branch: the scanner is built from main.
#
# Environment:
#   REPO, GH_TOKEN, OWNERS, PR_NUMBER, BRANCH, MODE
#   OUT            the build job's results (may be missing or empty)
#   BUILD_RESULT   the build job's result: success, failure or cancelled
#   SCANNER        the privacy scanner, built from main
#
# The messages use Markdown backticks inside single quotes on purpose.
# shellcheck disable=SC2016
set -euo pipefail

: "${REPO:?}" "${GH_TOKEN:?}" "${OWNERS:?}" "${PR_NUMBER:?}" "${BRANCH:?}" "${MODE:?}"
: "${OUT:?}" "${BUILD_RESULT:=unknown}" "${SCANNER:?}"
SERVER=${GITHUB_SERVER_URL:-https://github.com}
RUN_URL="$SERVER/$REPO/actions/runs/${GITHUB_RUN_ID:-}"
TMP=${RUNNER_TEMP:-/tmp}/publish
mkdir -p "$TMP"
MAX=60000 # GitHub caps a comment or description at 65,536 characters

[[ "$BRANCH" =~ ^spec/([0-9]{4}-[a-z0-9-]+)$ ]] || { echo "Not a build branch: $BRANCH"; exit 1; }
SPEC=docs/specs/${BASH_REMATCH[1]}.md

# A PR stopped while the run went on is left alone.
state=$(gh pr view "$PR_NUMBER" --repo "$REPO" --json state --jq .state)
if [ "$state" != OPEN ]; then
	echo "PR $PR_NUMBER is $state; nothing to publish."
	exit 0
fi

mentions=$(jq -r 'map("@" + .) | join(" ")' <<<"$OWNERS")
clip() { head -c "$MAX" "$1"; }
comment() { gh pr comment "$PR_NUMBER" --repo "$REPO" --body-file - >/dev/null; }
label() { # label ADD: the label to end on; building and the others come off
	local l args=(--add-label "$1")
	for l in building needs-owner-input final-review; do
		[ "$l" = "$1" ] || args+=(--remove-label "$l")
	done
	gh pr edit "$PR_NUMBER" --repo "$REPO" "${args[@]}" >/dev/null
}
# stop_publish MESSAGE: tell the owners, without anything from the build.
stop_publish() {
	printf '%s %s\n\nThe run: %s. Comment `/continue` to try again, or `/restart` to start over.\n' "$mentions" "$1" "$RUN_URL" | comment
	label needs-owner-input
	echo "::error::$1"
	exit 0
}

[ -f "$OUT/result.env" ] || stop_publish "The build run ended ($BUILD_RESULT) without leaving results, so nothing was pushed or posted from it."

# result.env is read field by field, never sourced.
field() { sed -n "s/^$1=//p" "$OUT/result.env" | head -n 1; }
status=$(field STATUS)
round=$(field ROUND)
head=$(field HEAD)
base=$(field BASE)
[[ "$status" =~ ^(pass|ask|error)$ && "$round" =~ ^[0-9]{1,2}$ &&
	"$head" =~ ^[0-9a-f]{40}$ && "$base" =~ ^[0-9a-f]{40}$ &&
	"$(field BRANCH)" == "$BRANCH" ]] || stop_publish "The build's results weren't in the expected shape, so nothing was pushed or posted from it."

# A repo of its own for the build's commits, checked out but never run.
git init -q "$TMP/repo"
cd "$TMP/repo"
auth="AUTHORIZATION: basic $(printf 'x-access-token:%s' "$GH_TOKEN" | base64 -w0)"
git_() { git -c "http.$SERVER/.extraheader=$auth" "$@"; }
git remote add origin "$SERVER/$REPO.git"
git_ fetch -q origin main "$BRANCH"
git fetch -q "$OUT/branch.bundle" "refs/heads/$BRANCH:refs/build/head" ||
	stop_publish "The build's commits couldn't be read, so nothing was pushed or posted from it."
[ "$(git rev-parse refs/build/head)" = "$head" ] ||
	stop_publish "The build's commits didn't match its results, so nothing was pushed or posted from it."

# What the implementer may not change (build.sh reverts these too).
protected=$(
	git diff --name-only origin/main...refs/build/head -- .github docs/specs internal/tools/privacyscan
	git diff --name-only --diff-filter=MD origin/main...refs/build/head -- docs/decisions ':!docs/decisions/README.md'
)
[ -z "$protected" ] || stop_publish "The build changed files it may not change ($(tr '\n' ' ' <<<"$protected")), so nothing was pushed or posted from it."

# The privacy scan's general rules on the commits and on every text. The
# findings name the rule and the place, never the text.
git checkout -q --detach refs/build/head
: >"$TMP/scan.txt"
"$SCANNER" -commits origin/main..refs/build/head >>"$TMP/scan.txt" 2>/dev/null || true
# The texts are scanned from an empty repo, since the scanner also scans
# the tree it's run in.
git init -q "$TMP/empty"
for f in "$OUT"/comments/*.md "$OUT/message.md" "$OUT/decisions.md"; do
	[ -f "$f" ] || continue
	(cd "$TMP/empty" && BUILD_TEXT=$(clip "$f") "$SCANNER" -text-env BUILD_TEXT 2>/dev/null) |
		sed "s|^BUILD_TEXT|${f#"$OUT"/}|" >>"$TMP/scan.txt" || true
done
if [ -s "$TMP/scan.txt" ]; then
	stop_publish "$(printf 'The privacy scan flagged the build'"'"'s output, so nothing was pushed or posted from it:\n\n```\n%s\n```' "$(head -n 50 "$TMP/scan.txt")")"
fi

# Push. The lease means the branch must still be where the build found it.
if [ "$head" != "$base" ]; then
	git_ push -q --force-with-lease="refs/heads/$BRANCH:$base" origin "refs/build/head:refs/heads/$BRANCH" ||
		stop_publish "The branch moved while the build ran, so its commits weren't pushed (nor its comments posted)."
fi

for f in "$OUT"/comments/*.md; do
	[ -f "$f" ] || continue
	clip "$f" | comment
done
{
	printf '%s ' "$mentions"
	clip "$OUT/message.md"
} | comment

case "$status" in
pass) state_line="Ready for an owner's final review (round $round)." ;;
ask) state_line="Waiting for an owner's answer (round $round)." ;;
error) state_line="The last run failed (round $round); see the comments." ;;
esac
{
	printf 'Built from [%s](%s/%s/blob/main/%s) by the build workflow (decision 0011).\n\n' "$SPEC" "$SERVER" "$REPO" "$SPEC"
	printf '**Status:** %s\n\n' "$state_line"
	printf '## Decisions\n\nThe calls the build made that the spec didn'"'"'t settle.\n\n<!-- decisions -->\n'
	grep -vF -e '<!-- decisions -->' -e '<!-- /decisions -->' "$OUT/decisions.md" | head -c 20000
	printf '\n<!-- /decisions -->\n\n'
	printf '## Commands\n\nAs the whole first line of a comment, from an owner:\n'
	printf -- '- `/continue`: carry on; put answers and guidance below the command.\n'
	printf -- '- `/restart`: throw the branch away and build again from the spec and this thread.\n'
	printf -- '- `/stop`: close the PR and stop any build that'"'"'s running.\n\n'
	printf '🤖 Built by the build workflow with Claude Code\n'
} >"$TMP/body.md"
gh pr edit "$PR_NUMBER" --repo "$REPO" --body-file "$TMP/body.md" >/dev/null

if [ "$status" = pass ]; then
	label final-review
	# Pushes made with the workflow's token don't start CI.
	gh workflow run ci.yml --repo "$REPO" --ref "$BRANCH"
else
	label needs-owner-input
fi
echo "Published round $round of $BRANCH: $status."
