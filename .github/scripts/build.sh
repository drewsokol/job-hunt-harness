#!/usr/bin/env bash
# Builds a feature from its spec: implement, check, review, fix, for up to
# MAX_ROUNDS rounds (decision 0011). Run by the build job in
# .github/workflows/build.yml, after the prepare job has made the branch and
# the PR.
#
# This job holds no token that can write to GitHub: it only reads. It runs
# Claude and the code Claude writes, and leaves everything for the publish
# job (.github/scripts/publish.sh) in $OUT: the branch as a git bundle, the
# review comments in order, the final message, the decisions and the
# status. The publish job, which can write, runs only trusted code and
# checks what it's given before it pushes or posts.
#
# Layout: trusted/ is main, where this script and the prompts come from;
# work/ is the repo the feature branch is built in.
#
# Environment:
#   MODE           start, continue or restart
#   PR_NUMBER      the build PR
#   BRANCH         its branch, spec/NNNN-name
#   OWNERS         JSON array of the owners' logins
#   REPO           owner/name
#   READ_TOKEN     a read-only token, for reading the PR's comments
#   OUT            where to leave the results
#   CLAUDE_CODE_OAUTH_TOKEN, MODEL, MAX_ROUNDS, BUDGET_MINUTES
#
# The messages use Markdown backticks inside single quotes on purpose.
# shellcheck disable=SC2016
set -euo pipefail

: "${MODE:?}" "${PR_NUMBER:?}" "${BRANCH:?}" "${OWNERS:?}" "${REPO:?}"
: "${READ_TOKEN:?}" "${OUT:?}" "${CLAUDE_CODE_OAUTH_TOKEN:?}"
: "${MAX_ROUNDS:=10}"
: "${MODEL:=claude-opus-5-5}"
# A run stops starting new rounds after this long, so it ends cleanly
# before the job's timeout.
: "${BUDGET_MINUTES:=200}"
TRUSTED=$PWD/trusted
WORK=$PWD/work
TMP=${RUNNER_TEMP:-/tmp}/build
RUN_URL="${GITHUB_SERVER_URL:-https://github.com}/${REPO}/actions/runs/${GITHUB_RUN_ID:-}"
BOT='github-actions[bot]'
mkdir -p "$TMP" "$OUT/comments"
started=$(date +%s)

# Nothing Claude runs, and none of the checks, get the read token; the
# checks don't get Claude's token either.
gh_() { GH_TOKEN="$READ_TOKEN" gh "$@"; }
claude_() { # claude_ MINUTES ARGS...
	local m=$1
	shift
	env -u READ_TOKEN -u GH_TOKEN -u GITHUB_TOKEN timeout "${m}m" claude "$@"
}
clean_env() { env -u READ_TOKEN -u GH_TOKEN -u GITHUB_TOKEN -u CLAUDE_CODE_OAUTH_TOKEN "$@"; }

# section NAME FILE: the lines under "## NAME" up to the next "## " heading.
section() { awk -v h="## $1" '$0 == h {on=1; next} /^## / {on=0} on' "$2" | sed '/./,$!d'; }

ncomment=0
add_comment() { # add_comment FILE: a comment for the publish job to post, in order
	ncomment=$((ncomment + 1))
	cp "$1" "$OUT/comments/$(printf '%03d' "$ncomment").md"
}

# finish STATUS ROUND MESSAGE_FILE: leave the results and end. STATUS is
# pass, ask (waiting for an owner) or error (the script failed; what was
# committed before is kept).
finish() {
	cp "$TMP/decisions.md" "$OUT/decisions.md"
	cp "$3" "$OUT/message.md"
	# Nothing that leaves this job may hold a token: not the texts, not the
	# commit messages. If one does, leave nothing; publish then tells the
	# owners the run ended without results.
	if { cat "$OUT"/comments/*.md "$OUT/message.md" "$OUT/decisions.md" 2>/dev/null
		git -C "$WORK" log -p --text "$base..refs/heads/$BRANCH"; } |
		grep -qF -e "$CLAUDE_CODE_OAUTH_TOKEN" -e "$READ_TOKEN"; then
		echo "::error::The build's output contains a token; leaving no results."
		rm -rf "${OUT:?}"/*
		leave_nothing=1
		exit 1
	fi
	git -C "$WORK" bundle create -q "$OUT/branch.bundle" "refs/heads/$BRANCH"
	{
		echo "STATUS=$1"
		echo "ROUND=$2"
		echo "BRANCH=$BRANCH"
		echo "BASE=$base"
		echo "HEAD=$(git -C "$WORK" rev-parse "refs/heads/$BRANCH")"
	} >"$OUT/result.env"
	exit 0
}

# If the script fails, leave the rounds already committed, so they aren't
# lost with the runner.
# shellcheck disable=SC2329 # run by the trap
on_exit() {
	local st=$?
	[ "$st" != 0 ] && [ ! -f "$OUT/result.env" ] && [ -n "${base:-}" ] && [ -z "${leave_nothing:-}" ] || return 0
	git -C "$WORK" reset -q --hard || true
	# Failed before switching to the branch: leave it as it was.
	git -C "$WORK" rev-parse -q --verify "refs/heads/$BRANCH" >/dev/null ||
		git -C "$WORK" branch -f "$BRANCH" "$base" || true
	printf 'The build script failed in round %s (the run log has the details: %s). The rounds it finished are pushed. Comment `/continue` to try again, or `/restart` to start over.\n' "${round:-0}" "$RUN_URL" >"$TMP/msg.md"
	(finish error "${round:-0}" "$TMP/msg.md") || true
}
trap on_exit EXIT

git -C "$WORK" config user.name "$BOT"
git -C "$WORK" config user.email "41898282+github-actions[bot]@users.noreply.github.com"

[[ "$BRANCH" =~ ^spec/([0-9]{4}-[a-z0-9-]+)$ ]] || { echo "Not a build branch: $BRANCH"; exit 1; }
slug=${BASH_REMATCH[1]}
SPEC=docs/specs/$slug.md
[ -f "$WORK/$SPEC" ] || { echo "No spec at $SPEC on main"; exit 1; }
git -C "$WORK" fetch -q origin "$BRANCH"
# The publish job pushes only if the branch is still here.
base=$(git -C "$WORK" rev-parse "origin/$BRANCH")

echo "None yet." >"$TMP/decisions.md"
: >"$TMP/fix.md"
: >"$TMP/owners.md"
: >"$TMP/last.md"

# What the owners have said on the PR. Nobody else's comments are passed on.
gh_ api "repos/$REPO/issues/$PR_NUMBER/comments" --paginate >"$TMP/comments.json"
jq -s --argjson owners "$OWNERS" -r '
	add // [] | map(select(.user.login as $u | $owners | index($u))) |
	map("### From \(.user.login), \(.created_at)\n\n\(.body)\n") | join("\n")' "$TMP/comments.json" >"$TMP/owners.md"

case "$MODE" in
start)
	# The prepare job made the branch: main plus an empty commit. Main may
	# have moved since, if this build waited for another, so bring it in:
	# put_back and the checks compare with main as it is now.
	git -C "$WORK" switch -q -C "$BRANCH" "origin/$BRANCH"
	git -C "$WORK" merge -q --no-edit origin/main
	TASK="Build the spec from scratch."
	;;
restart)
	git -C "$WORK" switch -q -C "$BRANCH" origin/main
	git -C "$WORK" commit -q --allow-empty -m "Start building spec $slug again"
	TASK="Build the spec from scratch. An earlier attempt was thrown away; the owners' comments say what to do differently."
	;;
continue)
	# The build's own latest review, posted by the publish job.
	jq -s -r --arg bot "$BOT" 'add // [] |
		map(select(.user.login == $bot and (.body | startswith("<!-- build-review -->")))) |
		last | .body // ""' "$TMP/comments.json" >"$TMP/fix.md"
	# Its last message to the owners (with any questions they answered).
	jq -s -r --arg bot "$BOT" 'add // [] |
		map(select(.user.login == $bot and (.body | startswith("<!-- build-message -->")))) |
		last | .body // ""' "$TMP/comments.json" >"$TMP/last.md"
	gh_ pr view "$PR_NUMBER" --repo "$REPO" --json body --jq .body |
		awk '/<!-- decisions -->/{on=1; next} /<!-- \/decisions -->/{on=0} on' >"$TMP/prev.md"
	[ ! -s "$TMP/prev.md" ] || cp "$TMP/prev.md" "$TMP/decisions.md"
	git -C "$WORK" switch -q -C "$BRANCH" "origin/$BRANCH"
	if ! git -C "$WORK" merge -q --no-edit origin/main; then
		git -C "$WORK" merge --abort
		echo 'The branch conflicts with main, so the build can'"'"'t carry on. Comment `/restart` to build it again from the spec and this thread.' >"$TMP/msg.md"
		finish ask 0 "$TMP/msg.md"
	fi
	TASK="Carry on with the build. The owners answered or commented since the run stopped; follow their answers, and fix what the latest review (under To fix) found."
	;;
*)
	echo "Unknown mode $MODE"
	exit 1
	;;
esac

checks() {
	(
		cd "$WORK"
		set +e
		out=$(gofmt -l . 2>&1)
		[ -z "$out" ] || { echo "gofmt: not formatted:"; echo "$out"; exit 1; }
		clean_env go vet ./... 2>&1 || exit 1
		clean_env go tool staticcheck ./... 2>&1 || exit 1
		clean_env go test ./... >"$TMP/test.out" 2>&1
		st=$?
		grep -v '^ok \|no test files' "$TMP/test.out"
		[ "$st" = 0 ] || exit 1
		clean_env go run ./internal/tools/privacyscan -commits origin/main..HEAD 2>&1 || exit 1
	) >"$TMP/checks.txt"
}

# put_back: undo the implementer's changes to what it may not change: the
# workflows, the specs, the privacy scanner, Claude Code's settings, and old
# decision records (new records and the decisions index are allowed).
put_back() {
	git -C "$WORK" checkout -q origin/main -- .github docs/specs internal/tools/privacyscan
	# .claude may not exist on main; its settings would load for the next
	# round's Claude, and for the review workflows once merged.
	git -C "$WORK" rm -rq --cached --ignore-unmatch -- .claude
	git -C "$WORK" checkout -q origin/main -- .claude 2>/dev/null || true
	git -C "$WORK" clean -qfdx -- .github docs/specs internal/tools/privacyscan .claude
	git -C "$WORK" diff --name-only --diff-filter=MD origin/main -- docs/decisions ':!docs/decisions/README.md' |
		while read -r f; do git -C "$WORK" checkout -q origin/main -- "$f"; done
}

# commit_round ROUND SUMMARY: commit what the implementer left, if anything.
commit_round() {
	put_back
	git -C "$WORK" add -A
	if git -C "$WORK" diff --cached --text | grep -qF -e "$CLAUDE_CODE_OAUTH_TOKEN" -e "$READ_TOKEN"; then
		echo "The change contains a token; throwing the round away."
		git -C "$WORK" reset -q --hard
		echo 'The build stopped: the change contained a token, so this round was thrown away. Look at the run log, then `/continue` or `/restart`.' >"$TMP/msg.md"
		finish ask "$1" "$TMP/msg.md"
	fi
	git -C "$WORK" diff --cached --quiet && return 0
	git -C "$WORK" commit -q -F - <<EOF
Build $slug, round $1: ${2:-changes}

Co-Authored-By: Claude <noreply@anthropic.com>
EOF
}

# post_failed_checks: when a run ends right after failed checks, post
# their output as the round's review, so /continue picks it up as the work
# to fix.
post_failed_checks() {
	[ -s "$TMP/fix.md" ] && ! grep -q '^<!-- build-review -->' "$TMP/fix.md" || return 0
	{
		echo "<!-- build-review -->"
		echo "**Round $round checks failed** (the run ended before fixing them)"
		echo
		cat "$TMP/fix.md"
	} >"$TMP/review-comment.md"
	add_comment "$TMP/review-comment.md"
}

round=0
while [ "$round" -lt "$MAX_ROUNDS" ]; do
	if [ $(($(date +%s) - started)) -gt $((BUDGET_MINUTES * 60)) ]; then
		post_failed_checks
		printf 'The build used its time for one run after %s rounds. The latest review is above. Comment `/continue` to carry on in a new run.\n' "$round" >"$TMP/msg.md"
		finish ask "$round" "$TMP/msg.md"
	fi
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
		if [ -s "$TMP/last.md" ]; then
			echo
			echo "## The build's last message to the owners"
			echo
			cat "$TMP/last.md"
		fi
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
	if ! (cd "$WORK" && claude_ 75 -p --model "$MODEL" --max-turns 250 --output-format text \
		--allowedTools "Read,Edit,Write,MultiEdit,Glob,Grep,TodoWrite,Bash(go:*),Bash(gofmt:*),Bash(git status:*),Bash(git diff:*),Bash(git log:*)" \
		<"$TMP/implement-prompt.md") >"$TMP/implement-out.md"; then
		echo "::endgroup::"
		# Keep what it got done, so /continue starts from there.
		commit_round "$round" "unfinished (the implementer stopped)"
		printf 'The implementer stopped unexpectedly in round %s (it failed or ran out of time; the run log has the details: %s). What it had done is pushed. Comment `/continue` to carry on from there.\n' "$round" "$RUN_URL" >"$TMP/msg.md"
		finish ask "$round" "$TMP/msg.md"
	fi
	echo "::endgroup::"

	status=$(grep -m1 -oE '^STATUS: (DONE|ASK)' "$TMP/implement-out.md" | cut -d' ' -f2 || true)
	section Decisions "$TMP/implement-out.md" >"$TMP/new-decisions.md"
	[ ! -s "$TMP/new-decisions.md" ] || cp "$TMP/new-decisions.md" "$TMP/decisions.md"
	commit_round "$round" "$(section Summary "$TMP/implement-out.md" | head -n 1)"

	if [ "$status" != DONE ]; then
		{
			echo "The build needs your input before it can carry on (round $round)."
			echo
			if [ "$status" = ASK ]; then
				section Questions "$TMP/implement-out.md"
			else
				echo "The implementer finished without saying whether it was done; the run log has its last message: $RUN_URL"
			fi
			echo
			echo 'Answer in a comment that starts with `/continue` on its first line.'
		} >"$TMP/msg.md"
		finish ask "$round" "$TMP/msg.md"
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
	if ! (cd "$TRUSTED" && claude_ 40 -p --model "$MODEL" --max-turns 150 --output-format text \
		--add-dir "$WORK" --add-dir "$TMP" --allowedTools "Read,Grep,Glob" \
		<"$TMP/review-prompt.md") >"$TMP/review-out.md"; then
		echo "::endgroup::"
		printf 'The reviewer stopped unexpectedly in round %s (the run log has the details: %s). The round'"'"'s code is pushed. Comment `/continue` to carry on.\n' "$round" "$RUN_URL" >"$TMP/msg.md"
		finish ask "$round" "$TMP/msg.md"
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
	add_comment "$TMP/review-comment.md"
	cp "$TMP/review-comment.md" "$TMP/fix.md"

	case "$verdict" in
	PASS)
		printf 'This build is ready for your final review. The checks pass and the review passed in round %s; the decisions it made are in the description.\n' "$round" >"$TMP/msg.md"
		finish pass "$round" "$TMP/msg.md"
		;;
	ASK)
		printf 'The reviewer needs your input on the question in the review above (round %s). Answer in a comment that starts with `/continue` on its first line.\n' "$round" >"$TMP/msg.md"
		finish ask "$round" "$TMP/msg.md"
		;;
	esac
	TASK="Carry on with the build: fix what the review under To fix found."
done

post_failed_checks
printf 'The build ran %s rounds without a passing review. The latest review is above. Comment `/continue` to keep going (with any guidance below the command), `/restart` to start over, or `/stop`.\n' "$MAX_ROUNDS" >"$TMP/msg.md"
finish ask "$round" "$TMP/msg.md"
