#!/usr/bin/env bash
# Decides what the build workflow builds, and gets the branch and PR ready
# (decision 0011). Run by the prepare job in .github/workflows/build.yml,
# with a token that can write. It runs no code from the repo and calls only
# GitHub's API, so nothing Claude wrote runs next to that token.
#
# For a new spec it makes the branch spec/NNNN-name (main plus an empty
# commit) and opens the PR, labeled building. For /continue and /restart it
# checks the PR is a build PR from this repo, reacts to the comment and
# labels the PR building.
#
# Environment:
#   EVENT          push, workflow_dispatch or issue_comment
#   REPO           owner/name
#   GH_TOKEN       the workflow's token
#   INPUT_SPEC     workflow_dispatch: the spec's path
#   BEFORE, AFTER  push: the commits before and after it
#   COMMENT_BODY, COMMENT_ID, ISSUE   issue_comment
#
# Outputs (to $GITHUB_OUTPUT): mode (none, start, continue or restart), pr
# and branch.
#
# The messages use Markdown backticks inside single quotes on purpose.
# shellcheck disable=SC2016
set -euo pipefail

: "${EVENT:?}" "${REPO:?}" "${GH_TOKEN:?}"
SERVER=${GITHUB_SERVER_URL:-https://github.com}
summary() { echo "$*" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"; }
none() {
	summary "$1"
	echo "mode=none" >>"$GITHUB_OUTPUT"
	exit 0
}
react() { gh api "repos/$REPO/issues/comments/$COMMENT_ID/reactions" -f content="$1" >/dev/null; }

labels() {
	gh label create building --repo "$REPO" --force --color 1d76db \
		--description "The build workflow is working on this PR" >/dev/null
	gh label create needs-owner-input --repo "$REPO" --force --color d93f0b \
		--description "The build workflow is waiting for an owner's answer" >/dev/null
	gh label create final-review --repo "$REPO" --force --color 0e8a16 \
		--description "Built and reviewed; ready for an owner's final review" >/dev/null
}

start() { # start SPEC
	local spec=$1 slug branch title main tree commit url pr
	[[ "$spec" =~ ^docs/specs/([0-9]{4}-[a-z0-9-]+)\.md$ ]] || none "Not a spec path: $spec"
	slug=${BASH_REMATCH[1]}
	branch=spec/$slug
	title=$(gh api -H "Accept: application/vnd.github.raw" "repos/$REPO/contents/$spec?ref=main" | head -n 1 | sed 's/^# *//') ||
		none "No spec on main at $spec."
	if gh api "repos/$REPO/git/ref/heads/$branch" >/dev/null 2>&1; then
		none "The branch $branch already exists. Comment \`/restart\` on its PR to build it again, or delete the branch and run this again."
	fi
	labels
	main=$(gh api "repos/$REPO/git/ref/heads/main" --jq .object.sha)
	tree=$(gh api "repos/$REPO/git/commits/$main" --jq .tree.sha)
	# An empty first commit, so the PR can open before any code is written.
	commit=$(gh api "repos/$REPO/git/commits" -f message="Start building spec $slug" \
		-f tree="$tree" -f "parents[]=$main" --jq .sha)
	gh api "repos/$REPO/git/refs" -f ref="refs/heads/$branch" -f sha="$commit" >/dev/null
	url=$(gh pr create --repo "$REPO" --base main --head "$branch" --label building \
		--title "Build: $title" --body "$(
			printf 'Building [%s](%s/%s/blob/main/%s) (decision 0011). The build workflow is working on it; it will comment here when it needs an answer or is ready for review.\n\n🤖 Built by the build workflow with Claude Code\n' \
				"$spec" "$SERVER" "$REPO" "$spec"
		)")
	pr=${url##*/}
	summary "Building $spec on $branch: $url"
	{
		echo "mode=start"
		echo "pr=$pr"
		echo "branch=$branch"
	} >>"$GITHUB_OUTPUT"
}

case "$EVENT" in
push)
	: "${BEFORE:?}" "${AFTER:?}"
	specs=$(gh api "repos/$REPO/compare/$BEFORE...$AFTER" \
		--jq '.files[] | select(.status == "added") | .filename' |
		grep -E '^docs/specs/[0-9]{4}-[a-z0-9-]+\.md$' || true)
	[ -n "$specs" ] || none "No new spec in this push."
	first=$(head -n 1 <<<"$specs")
	if [ "$(wc -l <<<"$specs")" -gt 1 ]; then
		summary "More than one new spec; building $first. Start the others from the Actions tab once it's done:"
		tail -n +2 <<<"$specs" | tee -a "${GITHUB_STEP_SUMMARY:-/dev/null}"
	fi
	start "$first"
	;;
workflow_dispatch)
	start "${INPUT_SPEC:?}"
	;;
issue_comment)
	: "${COMMENT_BODY:?}" "${COMMENT_ID:?}" "${ISSUE:?}"
	first=$(printf '%s\n' "$COMMENT_BODY" | head -n 1 | tr -d '\r' | sed 's/[[:space:]]*$//')
	mode=${first#/}
	case "$mode" in continue | restart) ;; *) none "Not a build command." ;; esac
	read -r head fork labels < <(gh pr view "$ISSUE" --repo "$REPO" --json headRefName,isCrossRepository,labels \
		--jq '.headRefName + " " + (.isCrossRepository | tostring) + " ," + ([.labels[].name] | join(",")) + ","')
	if [[ ! "$head" =~ ^spec/[0-9]{4}-[a-z0-9-]+$ || "$fork" != false ]]; then
		react confused
		none "PR $ISSUE isn't a build PR."
	fi
	if [[ "$labels" == *,building,* ]]; then
		react confused
		gh pr comment "$ISSUE" --repo "$REPO" --body 'A build is already running or waiting for this PR, so this command was skipped. Comment `/stop` to stop it, or wait for it to finish. (If no build is running, remove the `building` label and comment again.)' >/dev/null
		none "PR $ISSUE is already building."
	fi
	react rocket
	labels
	gh pr edit "$ISSUE" --repo "$REPO" --add-label building \
		--remove-label needs-owner-input --remove-label final-review >/dev/null
	{
		echo "mode=$mode"
		echo "pr=$ISSUE"
		echo "branch=$head"
	} >>"$GITHUB_OUTPUT"
	;;
*)
	none "Unknown event $EVENT."
	;;
esac
