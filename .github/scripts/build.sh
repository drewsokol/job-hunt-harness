#!/usr/bin/env bash
# Builds a feature from its spec: implement, check, review, fix, for up to
# MAX_ROUNDS rounds (decision 0011). Run by .github/workflows/build.yml.
#
# Layout: trusted/ is main, where this script and the prompts come from;
# work/ is the repo the feature branch is built in. Claude never holds a
# GitHub token: this script does all of GitHub's reading and writing, and
# gives Claude only the spec, the owners' comments and the reviews.
#
# Environment:
#   MODE           start, continue or restart
#   SPEC           start only: the spec's path, docs/specs/NNNN-name.md
#   PR_NUMBER      continue and restart: the build PR
#   OWNERS         JSON array of the owners' logins
#   REPO           owner/name
#   BUILD_TOKEN    the workflow's token, for gh and git push
#   CLAUDE_CODE_OAUTH_TOKEN, MODEL, MAX_ROUNDS
#
# The messages use Markdown backticks inside single quotes on purpose.
# shellcheck disable=SC2016
set -euo pipefail

: "${MODE:?}" "${OWNERS:?}" "${REPO:?}" "${BUILD_TOKEN:?}"
: "${MAX_ROUNDS:=10}"
: "${MODEL:=claude-opus-5-5}"
TRUSTED=$PWD/trusted
WORK=$PWD/work
TMP=${RUNNER_TEMP:-/tmp}/build
RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${REPO}/actions/runs/${GITHUB_RUN_ID:-}"
mkdir -p "$TMP"
[ -n "${CLAUDE_CODE_OAUTH_TOKEN:-}" ] || { echo "CLAUDE_CODE_OAUTH_TOKEN isn't set"; exit 1; }

mentions=$(jq -r 'map("@" + .) | join(" ")' <<<"$OWNERS")

gh_() { GH_TOKEN="$BUILD_TOKEN" gh "$@"; }
# Claude and everything it runs get no GitHub token.
claude_() { env -u BUILD_TOKEN -u GH_TOKEN -u GITHUB_TOKEN timeout 100m claude "$@"; }
push_() { git -C "$WORK" push -q "https://x-access-token:${BUILD_TOKEN}@github.com/${REPO}.git" "$@"; }

comment() { gh_ pr comment "$PR" --repo "$REPO" --body-file "$1" >/dev/null; }
label() { # label ADD [REMOVE...]
	local add=$1
	shift
	local args=(--add-label "$add")
	for r in "$@"; do args+=(--remove-label "$r"); done
	gh_ pr edit "$PR" --repo "$REPO" "${args[@]}" >/dev/null
}

# section NAME FILE: the lines under "## NAME" up to the next "## " heading.
section() { awk -v h="## $1" '$0 == h {on=1; next} /^## / {on=0} on' "$2" | sed '/./,$!d'; }

pr_body() { # pr_body ROUND STATUS_TEXT, with the decisions from $TMP/decisions.md
	{
		echo "Builds [spec $slug](../blob/main/$SPEC) (decision 0011)."
		echo
		echo "## Decisions the build made"
		echo
		echo "<!-- decisions -->"
		cat "$TMP/decisions.md"
		echo "<!-- /decisions -->"
		echo
		echo "## Status"
		echo
		echo "Round $1: $2"
		echo
		echo 'Owners steer it with a comment whose first line is a command: `/continue` (carry on, with the comments since as answers), `/stop` (close it), `/restart` (build again from the spec and this thread).'
		echo
		echo "🤖 Built by the build workflow with [Claude Code](https://claude.com/claude-code)"
	} >"$TMP/body.md"
	gh_ pr edit "$PR" --repo "$REPO" --body-file "$TMP/body.md" >/dev/null
}

# wait_for_owner ROUND MESSAGE_FILE: tag the owners, label the PR, end the run.
wait_for_owner() {
	comment "$2"
	pr_body "$1" "waiting for an owner's answer."
	label needs-owner-input building
	exit 0
}

for l in "building:1d76db:A build run is working on this" \
	"needs-owner-input:d93f0b:The build is waiting on an owner's answer" \
	"final-review:0e8a16:Built and reviewed; ready for an owner to merge"; do
	IFS=: read -r name color desc <<<"$l"
	gh_ label create "$name" --repo "$REPO" --color "$color" --description "$desc" --force >/dev/null
done

git -C "$WORK" config user.name "github-actions[bot]"
git -C "$WORK" config user.email "41898282+github-actions[bot]@users.noreply.github.com"

echo "None yet." >"$TMP/decisions.md"
PR=""
case "$MODE" in
start)
	[[ "$SPEC" =~ ^docs/specs/([0-9]{4}-[a-z0-9-]+)\.md$ ]] || { echo "Not a spec path: $SPEC"; exit 1; }
	slug=${BASH_REMATCH[1]}
	branch=spec/$slug
	[ -f "$WORK/$SPEC" ] || { echo "No such spec on main: $SPEC"; exit 1; }
	if git -C "$WORK" ls-remote --exit-code --heads origin "$branch" >/dev/null; then
		echo "Branch $branch already exists; comment /restart on its PR to build it again."
		exit 1
	fi
	git -C "$WORK" switch -q -c "$branch" origin/main
	# An empty first commit lets the PR open now, so there's a place for
	# questions even before any code.
	git -C "$WORK" commit -q --allow-empty -m "Start building spec $slug"
	push_ "HEAD:refs/heads/$branch"
	title=$(head -n 1 "$WORK/$SPEC" | sed 's/^# *//')
	echo "Starting." >"$TMP/body.md"
	PR=$(gh_ pr create --repo "$REPO" --base main --head "$branch" \
		--title "Build $title" --body-file "$TMP/body.md" --label building | grep -oE '[0-9]+$')
	echo "$PR" >"$TMP/pr" # for the workflow's failure step
	pr_body 0 "building."
	;;
continue | restart)
	PR=$PR_NUMBER
	echo "$PR" >"$TMP/pr" # for the workflow's failure step
	read -r branch state < <(gh_ pr view "$PR" --repo "$REPO" --json headRefName,state --jq '.headRefName + " " + .state')
	if ! [[ "$branch" =~ ^spec/([0-9]{4}-[a-z0-9-]+)$ && "$state" == OPEN ]]; then
		echo "PR $PR isn't an open build PR"
		exit 0
	fi
	slug=${BASH_REMATCH[1]}
	SPEC=docs/specs/$slug.md
	[ -f "$WORK/$SPEC" ] || { echo "No spec at $SPEC on main"; exit 1; }
	label building needs-owner-input final-review
	gh_ pr view "$PR" --repo "$REPO" --json body --jq .body |
		awk '/<!-- decisions -->/{on=1; next} /<!-- \/decisions -->/{on=0} on' >"$TMP/prev.md"
	[ ! -s "$TMP/prev.md" ] || cp "$TMP/prev.md" "$TMP/decisions.md"
	if [ "$MODE" = restart ]; then
		echo "None yet." >"$TMP/decisions.md"
		git -C "$WORK" switch -q -C "$branch" origin/main
		git -C "$WORK" commit -q --allow-empty -m "Start building spec $slug again"
		push_ --force "HEAD:refs/heads/$branch"
	else
		git -C "$WORK" fetch -q origin "$branch"
		git -C "$WORK" switch -q -C "$branch" "origin/$branch"
		if ! git -C "$WORK" merge -q --no-edit origin/main; then
			git -C "$WORK" merge --abort
			printf '%s: the branch conflicts with main, so the build can'"'"'t carry on. Comment `/restart` to build it again from the spec and this thread.\n' "$mentions" >"$TMP/c.md"
			wait_for_owner 0 "$TMP/c.md"
		fi
	fi
	;;
*)
	echo "Unknown mode $MODE"
	exit 1
	;;
esac

# What the owners have said on the PR, and the latest review, for the
# implementer. Nobody else's comments are passed on.
gh_ api "repos/$REPO/issues/$PR/comments" --paginate >"$TMP/comments.json"
jq -s --argjson owners "$OWNERS" -r '
	add // [] | map(select(.user.login as $u | $owners | index($u))) |
	map("### From \(.user.login), \(.created_at)\n\n\(.body)\n") | join("\n")' "$TMP/comments.json" >"$TMP/owners.md"
jq -s -r 'add // [] | map(select(.body | contains("<!-- build-review -->"))) | last | .body // ""' \
	"$TMP/comments.json" >"$TMP/fix.md"

case "$MODE" in
start) TASK="Build the spec from scratch." ;;
restart) TASK="Build the spec from scratch. An earlier attempt was thrown away; the owners' comments say what to do differently." ;;
continue) TASK="Carry on with the build. The owners answered or commented since the run stopped; follow their answers, and fix what the latest review (under To fix) found." ;;
esac

checks() {
	(
		cd "$WORK"
		set +e
		out=$(gofmt -l . 2>&1)
		[ -z "$out" ] || { echo "gofmt: not formatted:"; echo "$out"; exit 1; }
		go vet ./... 2>&1 || exit 1
		go tool staticcheck ./... 2>&1 || exit 1
		go test ./... >test.out 2>&1
		st=$?
		grep -v '^ok \|no test files' test.out
		rm -f test.out
		[ "$st" = 0 ] || exit 1
		go run ./internal/tools/privacyscan -commits origin/main..HEAD 2>&1 || exit 1
	) >"$TMP/checks.txt"
}

# put_back: undo the implementer's changes to what it may not change. New
# decision records and the decisions index are allowed; old records aren't.
put_back() {
	git -C "$WORK" checkout -q origin/main -- .github docs/specs
	git -C "$WORK" clean -qfd -- .github docs/specs
	git -C "$WORK" diff --name-only --diff-filter=MD origin/main -- docs/decisions ':!docs/decisions/README.md' |
		while read -r f; do git -C "$WORK" checkout -q origin/main -- "$f"; done
}

round=0
while [ "$round" -lt "$MAX_ROUNDS" ]; do
	round=$((round + 1))
	echo "::group::Round $round: implement"
	{
		cat "$TRUSTED/.github/prompts/implement.md"
		echo
		echo "SPEC: $SPEC"
		echo
		echo "# TASK"
		echo
		echo "$TASK"
		echo
		echo "## Decisions so far"
		echo
		cat "$TMP/decisions.md"
		if [ -s "$TMP/owners.md" ]; then
			echo
			echo "## The owners' comments"
			echo
			cat "$TMP/owners.md"
		fi
		if [ -s "$TMP/fix.md" ]; then
			echo
			echo "## To fix"
			echo
			cat "$TMP/fix.md"
		fi
	} >"$TMP/implement-prompt.md"
	if ! (cd "$WORK" && claude_ -p --model "$MODEL" --max-turns 250 --output-format text \
		--allowedTools "Read,Edit,Write,MultiEdit,Glob,Grep,TodoWrite,Bash(go:*),Bash(gofmt:*),Bash(git status:*),Bash(git diff:*),Bash(git log:*)" \
		<"$TMP/implement-prompt.md") >"$TMP/implement-out.md"; then
		echo "::endgroup::"
		printf '%s: the implementer stopped unexpectedly in round %s (it failed or ran out of time; the run log has the details: %s). Comment `/continue` to try again from where it got to.\n' "$mentions" "$round" "$RUN_URL" >"$TMP/c.md"
		wait_for_owner "$round" "$TMP/c.md"
	fi
	echo "::endgroup::"

	put_back
	git -C "$WORK" add -A
	if git -C "$WORK" diff --cached | grep -qF -- "$CLAUDE_CODE_OAUTH_TOKEN"; then
		echo "The change contains the Claude token; stopping without pushing."
		exit 1
	fi

	status=$(grep -m1 -oE '^STATUS: (DONE|ASK)' "$TMP/implement-out.md" | cut -d' ' -f2 || true)
	section Decisions "$TMP/implement-out.md" >"$TMP/new-decisions.md"
	[ ! -s "$TMP/new-decisions.md" ] || cp "$TMP/new-decisions.md" "$TMP/decisions.md"
	summary=$(section Summary "$TMP/implement-out.md" | head -n 1)

	if ! git -C "$WORK" diff --cached --quiet; then
		git -C "$WORK" commit -q -F - <<EOF
Build $slug, round $round: ${summary:-changes}

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
		push_ "HEAD:refs/heads/$branch"
	fi

	if [ "$status" != DONE ]; then
		{
			echo "$mentions: the build needs your input before it can carry on (round $round)."
			echo
			if [ "$status" = ASK ]; then
				section Questions "$TMP/implement-out.md"
			else
				echo "The implementer finished without saying whether it was done; the run log has its last message: $RUN_URL"
			fi
			echo
			echo 'Answer in a comment that starts with `/continue` on its first line.'
		} >"$TMP/c.md"
		wait_for_owner "$round" "$TMP/c.md"
	fi

	echo "::group::Round $round: checks"
	if ! checks; then
		cat "$TMP/checks.txt"
		echo "::endgroup::"
		{
			echo "The run's checks failed after round $round. Fix these:"
			echo
			echo '```'
			tail -n 150 "$TMP/checks.txt"
			echo '```'
		} >"$TMP/fix.md"
		TASK="Carry on with the build: fix what's under To fix."
		pr_body "$round" "fixing failed checks."
		continue
	fi
	echo "::endgroup::"

	echo "::group::Round $round: review"
	git -C "$WORK" diff origin/main...HEAD >"$TMP/diff.patch"
	{
		cat "$TRUSTED/.github/prompts/build-review.md"
		echo
		echo "BRANCH: $WORK"
		echo "DIFF: $TMP/diff.patch"
		echo "SPEC: $WORK/$SPEC"
		echo
		echo "DECISIONS:"
		cat "$TMP/decisions.md"
	} >"$TMP/review-prompt.md"
	if ! (cd "$TRUSTED" && claude_ -p --model "$MODEL" --max-turns 150 --output-format text \
		--add-dir "$WORK" --add-dir "$TMP" --allowedTools "Read,Grep,Glob" \
		<"$TMP/review-prompt.md") >"$TMP/review-out.md"; then
		echo "::endgroup::"
		printf '%s: the reviewer stopped unexpectedly in round %s (the run log has the details: %s). Comment `/continue` to try again.\n' "$mentions" "$round" "$RUN_URL" >"$TMP/c.md"
		wait_for_owner "$round" "$TMP/c.md"
	fi
	echo "::endgroup::"
	verdict=$(grep -oE '^VERDICT: (PASS|FIX|ASK)' "$TMP/review-out.md" | tail -n 1 | cut -d' ' -f2 || true)
	[ -n "$verdict" ] || verdict=FIX
	{
		echo "<!-- build-review -->"
		echo "**Round $round review** (by the build workflow's reviewer)"
		echo
		grep -vE '^VERDICT: ' "$TMP/review-out.md"
	} >"$TMP/review-comment.md"
	comment "$TMP/review-comment.md"
	cp "$TMP/review-comment.md" "$TMP/fix.md"

	case "$verdict" in
	PASS)
		pr_body "$round" "built, checked and reviewed; ready for an owner's final review."
		label final-review building
		printf '%s: this build is ready for your final review. The checks pass and the review passed in round %s; the decisions it made are in the description.\n' "$mentions" "$round" >"$TMP/c.md"
		comment "$TMP/c.md"
		# Pushes made with the workflow's token don't start CI, so start it.
		gh_ workflow run ci.yml --repo "$REPO" --ref "$branch"
		exit 0
		;;
	ASK)
		printf '%s: the reviewer needs your input on the question in the review above (round %s). Answer in a comment that starts with `/continue` on its first line.\n' "$mentions" "$round" >"$TMP/c.md"
		wait_for_owner "$round" "$TMP/c.md"
		;;
	esac
	TASK="Carry on with the build: fix what the review under To fix found."
	pr_body "$round" "fixing what the review found."
done

printf '%s: the build ran %s rounds without a passing review. The latest review is above. Comment `/continue` to keep going (with any guidance below the command), `/restart` to start over, or `/stop`.\n' "$mentions" "$MAX_ROUNDS" >"$TMP/c.md"
comment "$TMP/c.md"
pr_body "$round" "stopped after $MAX_ROUNDS rounds; waiting for an owner."
label needs-owner-input building
