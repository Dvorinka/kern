#!/bin/sh
# Host updater. Runs beside the API, not inside it, and recreates the api
# and compose runner containers. Fleet runners that set KERN_RUNNER_UPDATE=1
# replace themselves; this script does not touch them.
#
# ponytail: apk add on start, pin an updater image if the Alpine mirror is unreachable.
set -eu
apk add --no-cache git jq wget >/dev/null

API="${KERN_API_URL:-http://api:8080}"
TOKEN="${KERN_RUNNER_JOIN_TOKEN:?KERN_RUNNER_JOIN_TOKEN is required}"
SRC="${KERN_SRC:-/opt/kern/src}"
ENV_FILE="${KERN_ENV_FILE:-/opt/kern/.env}"
REPO="${KERN_RELEASE_REPO:-Dvorinka/kern}"

post() {
	body=$(jq -n --arg state "$1" --arg error "$2" --arg version "$3" \
		'{state:$state,error:$error,version:$version}')
	wget -q -O /dev/null \
		--header "Authorization: Bearer ${TOKEN}" \
		--header "Content-Type: application/json" \
		--post-data "$body" \
		"${API}/api/v1/update/status" || true
}

printf '%s' "$REPO" | grep -Eq '^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$' || {
	echo "KERN_RELEASE_REPO must look like owner/name" >&2
	exit 1
}
export GIT_TERMINAL_PROMPT=0
# The checkout is owned by the host user. This container is root.
export GIT_CONFIG_COUNT=1
export GIT_CONFIG_KEY_0=safe.directory
export GIT_CONFIG_VALUE_0="$SRC"

while true; do
	if ! wget -q -O /tmp/kern-plan.json \
		--header "Authorization: Bearer ${TOKEN}" \
		"${API}/api/v1/update/plan"; then
		sleep 30
		continue
	fi
	apply=$(jq -r '.apply // false' /tmp/kern-plan.json)
	ver=$(jq -r '.version // empty' /tmp/kern-plan.json)
	sha=$(jq -r '.git_sha // empty' /tmp/kern-plan.json)
	if [ "$apply" != "true" ]; then
		post idle "" "$ver"
		sleep 30
		continue
	fi
	if ! printf '%s' "$sha" | grep -Eq '^[0-9a-f]{40}$'; then
		post error "manifest git_sha is not 40 hex characters" "$ver"
		sleep 30
		continue
	fi
	if ! printf '%s' "$ver" | grep -Eq '^v?[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z]+)?$'; then
		post error "manifest version is not a dotted triple" "$ver"
		sleep 30
		continue
	fi
	if [ -n "$(git -C "$SRC" status --porcelain)" ]; then
		post error "checkout is dirty" "$ver"
		sleep 30
		continue
	fi
	# HEAD matching the manifest sha is not proof the running binary is at
	# that sha — a manual `git pull` or an interrupted deploy leaves the
	# checkout ahead of the image. Compare the API's own version report
	# before idling, or the host stays stale forever.
	running=$(printf '%s' "$(jq -r '.current // empty' /tmp/kern-plan.json)" | sed 's/^v//')
	target=$(printf '%s' "$ver" | sed 's/^v//')
	if [ "$(git -C "$SRC" rev-parse HEAD)" = "$sha" ] && [ "$running" = "$target" ]; then
		post idle "" "$ver"
		sleep 30
		continue
	fi
	post applying "" "$ver"
	if ! git -C "$SRC" fetch "https://github.com/${REPO}.git" "$sha"; then
		post error "git fetch failed" "$ver"
		sleep 30
		continue
	fi
	if ! git -C "$SRC" checkout master; then
		post error "checkout master failed" "$ver"
		sleep 30
		continue
	fi
	if ! git -C "$SRC" merge --ff-only FETCH_HEAD; then
		post error "not a fast-forward of master" "$ver"
		sleep 30
		continue
	fi
	# Same working directory as the operator recipe, so the compose project
	# name stays "deployments". The updater service is not in this command,
	# and neither is the profile-gated runner service.
	if ! (cd "$SRC/deployments" && KERN_VERSION="$ver" docker compose --env-file "$ENV_FILE" up -d --build --no-deps api); then
		post error "compose up failed" "$ver"
		sleep 30
		continue
	fi
	post idle "" "$ver"
	sleep 30
done
