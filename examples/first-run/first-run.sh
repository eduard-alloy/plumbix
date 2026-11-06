#!/usr/bin/env bash
# first-run.sh: from nothing to an approved verdict and a merge, on this
# machine, in a few minutes. It starts its own Plumbix server on a port of
# its own with its data under a directory of its own, so nothing you
# already run is touched; builds the reference validator image if it is
# missing; creates a bare git repository with the sample ledger and its
# pipeline; posts the acceptance criteria as a criteria set; pushes the
# change on a branch; submits it; waits for the verdict; merges it; and
# leaves the server running for you to look at, unless told --stop.
#
# Needs: Docker running, git, curl, and a plumbix binary (PLUMBIX_BIN, or
# plumbix on PATH, or bin/plumbix from make build in this checkout).
#
#   PLUMBIX_BIN           the binary (default: plumbix on PATH, else ../../bin/plumbix)
#   PLUMBIX_EXAMPLE_DIR   where the server's data and the repositories go
#                         (default: $TMPDIR/plumbix-first-run)
#   PLUMBIX_EXAMPLE_PORT  the port (default: 6410)
#   PLUMBIX_EXAMPLE_BUILD_IMAGE=yes
#                         build the validator image through the server even when
#                         it is present (the integration test does, to cover that path)
#   --stop                stop the server at the end (the integration test does).
#                         A run that fails stops its server in either mode.
#
# Running it again is fine: a server a previous run left on the port is
# stopped first, as long as it is this script's (its pid is kept under the
# directory); anything else on the port is an error, not a victim.
set -euo pipefail

here=$(cd "$(dirname "$0")" && pwd)
bin=${PLUMBIX_BIN:-$(command -v plumbix || true)}
[ -n "$bin" ] || bin=$here/../../bin/plumbix
dir=${PLUMBIX_EXAMPLE_DIR:-${TMPDIR:-/tmp}/plumbix-first-run}
port=${PLUMBIX_EXAMPLE_PORT:-6410}
addr=127.0.0.1:$port
stop=no
[ "${1:-}" = "--stop" ] && stop=yes
owner=${USER:-you}@$(hostname -s 2>/dev/null || echo mac)
start=$SECONDS

say() { printf '\n== %s\n' "$*"; }
fail() { echo "first-run: $*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || fail "$1 is needed"; }
# field NAME: the first string value of NAME in JSON on standard input,
# whether the JSON is compact (the API) or indented (the CLI).
field() { grep -o "\"$1\": *\"[^\"]*\"" | head -1 | sed 's/.*: *"//; s/"$//'; }
px() { "$bin" "$1" --server "$addr" --data-dir "$dir/data" "${@:2}"; }
api() { curl -sf -H "Authorization: Bearer $(cat "$dir/data/api-token")" "$@"; }

need docker; need git; need curl
[ -x "$bin" ] || fail "no plumbix binary at $bin: run make build in the checkout, or set PLUMBIX_BIN"
docker info >/dev/null 2>&1 || fail "Docker is not running"
[ -n "$dir" ] && [ "$dir" != "/" ] || fail "PLUMBIX_EXAMPLE_DIR must be a directory of its own"

say "A Plumbix server of its own on $addr, data under $dir"
# ours: the pid in serve.pid is alive and still the server this script
# started, recognised by the data directory on its command line, so a
# reused pid, another Plumbix server of the user's, or a binary named
# differently (a release download) is never mistaken for it.
ours() { [ -f "$dir/serve.pid" ] && ps -ww -p "$(cat "$dir/serve.pid")" -o command= 2>/dev/null | grep -qF -- " serve -data-dir $dir/data "; }
stop_server() { if ours; then kill "$(cat "$dir/serve.pid")" 2>/dev/null || true; fi; }
if ours; then
  echo "stopping the server a previous run left on $addr"
  stop_server
  for _ in $(seq 1 50); do curl -sf "http://$addr/api/v1/health" >/dev/null 2>&1 || break; sleep 0.2; done
fi
if curl -sf "http://$addr/api/v1/health" >/dev/null 2>&1; then
  fail "something else answers on $addr; set PLUMBIX_EXAMPLE_PORT to a free port"
fi
rm -rf "$dir/data" "$dir/git"
mkdir -p "$dir/data" "$dir/git"
"$bin" serve -data-dir "$dir/data" -listen "$addr" >"$dir/serve.log" 2>&1 &
pid=$!
echo $pid >"$dir/serve.pid"
on_exit() { code=$?; if [ $stop = yes ] || [ $code -ne 0 ]; then stop_server; fi; }
trap on_exit EXIT
for _ in $(seq 1 50); do curl -sf "http://$addr/api/v1/health" >/dev/null 2>&1 && break; kill -0 $pid 2>/dev/null || break; sleep 0.2; done
kill -0 $pid 2>/dev/null || fail "the server exited at once; see $dir/serve.log"
curl -sf "http://$addr/api/v1/health" >/dev/null || fail "the server did not come up; see $dir/serve.log"
echo "up (pid $pid); the API token is in $dir/data/api-token"

say "The reference validator image"
if [ "${PLUMBIX_EXAMPLE_BUILD_IMAGE:-no}" != yes ] && docker image inspect plumbix-reference-validator:dev >/dev/null 2>&1; then
  echo "plumbix-reference-validator:dev is present"
else
  echo "building plumbix-reference-validator:dev from the sources embedded in the server (a minute or two)"
  build=$(api -X POST "http://$addr/api/v1/images/plumbix-reference-validator/build" | field id)
  [ -n "$build" ] || fail "the build did not start; see $dir/serve.log"
  status=running
  for _ in $(seq 1 100); do
    status=$(api "http://$addr/api/v1/images/builds/$build" | field status)
    [ "$status" = running ] || break
    sleep 3
  done
  [ "$status" = succeeded ] || fail "the image build ended $status after five minutes at most; see $dir/data/images/builds"
  echo "built"
fi

say "A bare repository to push to, with the sample ledger and its pipeline on main"
export GIT_AUTHOR_NAME=first-run GIT_AUTHOR_EMAIL=first-run@example.invalid
export GIT_COMMITTER_NAME=first-run GIT_COMMITTER_EMAIL=first-run@example.invalid
git init -q --bare "$dir/git/ledger.git"
git clone -q "$dir/git/ledger.git" "$dir/git/work" 2>/dev/null
cd "$dir/git/work"
git checkout -q -B main
cp -R "$here/repo/." .
git add -A && git commit -qm "ledger: Balance, and the pipeline" && git push -q origin main
echo "main: $(git log --oneline -1)"

say "Register it"
repo=$(px repo add --host git --url "$dir/git/ledger.git" | field id)
[ -n "$repo" ] || fail "repo add failed"
echo "repository $repo"

say "The acceptance criteria, written before the code, as a criteria set"
set_id=$(sed "s|OWNER|$owner|" "$here/criteria.json" | px criteria add -f - | field id)
[ -n "$set_id" ] || fail "criteria add failed"
echo "criteria set $set_id"

say "The change: a test for Balance, on a branch"
git checkout -q -b balance-test
cp "$here/_change/ledger_test.go" .
git add -A && git commit -qm "test Balance" && git push -q origin balance-test
echo "balance-test: $(git log --oneline -1)"

say "Submit it, naming the criteria set"
change=$(sed -e "s|REPO_ID|$repo|" -e "s|CRITERIA_SET|$set_id|" -e "s|OWNER|$owner|" "$here/change.json" | px submit -f - | field id)
[ -n "$change" ] || fail "submit failed"
echo "change $change"

say "The pipeline runs: test in a container, then the reference validator judges the criteria"
# change wait blocks until the verdict and exits 0 approved, 1 fix or reject,
# 2 needs a person, 3 timeout; its JSON names the state either way.
state=$(px change wait "$change" --timeout 8m | field state) || true
echo "verdict: $state"
if [ "$state" != approved ]; then
  px change runs "$change" || true
  fail "the change ended $state, not approved"
fi

say "Merge: the validated tree lands on main"
merged=$(px change merge "$change" | field state)
echo "state: $merged"
[ "$merged" = merged ] || fail "merge ended $merged"
git fetch -q origin
echo "main now: $(git log --oneline -1 origin/main)"

say "first run complete in $((SECONDS - start)) s"
if [ $stop = yes ]; then
  echo "stopping the server"
else
  echo "The server is still running. Sign your browser in with the link below, then look at the change:"
  px login
  echo "Stop it with: kill \$(cat $dir/serve.pid)"
fi
