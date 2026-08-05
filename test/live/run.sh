#!/usr/bin/env bash
# Run the live checks against a real box.
#
#   test/live/run.sh root@192.168.1.1          # the R5S
#   test/live/run.sh operator@<ip> --client      # the ThinkPad
#   test/live/run.sh root@192.168.1.1 04 06    # only these cases
#
# Why this exists: every live validation this project has done was a throwaway
# script written from scratch, run by hand, with the output pasted back. That is
# why suspend/resume has never been tested — there was nowhere to add a case. And
# it matters more than it sounds: running things has found roughly every bug in
# this repo, and reading them has found roughly none.
#
# HOUSE RULE: a case must not touch production. Anything that needs to mutate
# works in /tmp on the box, and anything that needs an engine binds a spare
# NFQUEUE nothing diverts traffic to. A case that cannot honour that says so and
# skips itself.
set -uo pipefail

cd "$(dirname "$0")/../.."
TARGET=${1:?usage: run.sh user@host [case...]}
shift || true
REMOTE=/tmp/lotsman-live
SPARE_QNUM=${SPARE_QNUM:-299}
SSH="ssh -o ConnectTimeout=10 -o BatchMode=yes $TARGET"

pass=0; fail=0; skip=0
say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32mPASS\033[0m %s\n' "$*"; pass=$((pass+1)); }
bad()  { printf '   \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail+1)); }
meh()  { printf '   \033[33mSKIP\033[0m %s\n' "$*"; skip=$((skip+1)); }

wanted() { # no case list = all
  [ $# -eq 0 ] && return 0
  for w in "$@"; do [ "$w" = "$CASE" ] && return 0; done
  return 1
}

# ---------------------------------------------------------------- staging ----
say "staging on $TARGET"
arch=$($SSH 'uname -m') || { echo "cannot reach $TARGET"; exit 1; }
case "$arch" in
  aarch64|arm64) goarch=arm64 ;;
  x86_64)        goarch=amd64 ;;
  *) echo "unsupported arch $arch"; exit 1 ;;
esac
echo "   $arch -> linux/$goarch"

GOOS=linux GOARCH=$goarch ./scripts/build.sh lotsmand >/dev/null
# The pure-logic guarantees are compiled as TEST binaries and run ON the box, so
# they are checked against its real filesystem and architecture rather than a
# developer laptop's.
for pkg in ./pkg/rulesets ./pkg/flowseal ./pkg/strategyimport ./pkg/executor; do
  name=$(basename "$pkg")
  CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go test -c "$pkg" -o "dist/$name.test" >/dev/null
done
$SSH "mkdir -p $REMOTE" </dev/null
for f in dist/lotsmand dist/*.test; do
  $SSH "cat > $REMOTE/$(basename "$f") && chmod +x $REMOTE/$(basename "$f")" < "$f"
done
echo "   staged $(ls dist/*.test | wc -l | tr -d ' ') test binaries + lotsmand"

# ------------------------------------------------------------------ cases ----
CASE=01; if wanted "$@"; then
say "$CASE  the binary knows what it is"
  out=$($SSH "$REMOTE/lotsmand -config /dev/null -simulate 2>&1 | head -1" </dev/null)
  case "$out" in
    *'version="v'*) ok "$(echo "$out" | sed 's/.*version="\([^"]*\)".*/\1/')" ;;
    *version=dev*)  bad "reports 'dev' — the build did not stamp a version" ;;
    *)              bad "no version in the startup line: $out" ;;
  esac
fi

CASE=02; if wanted "$@"; then
say "$CASE  pure guarantees, on the box's own filesystem"
  for t in rulesets flowseal strategyimport executor; do
    if $SSH "cd $REMOTE && ./$t.test -test.count=1 >/tmp/$t.out 2>&1" </dev/null; then
      ok "$t"
    else
      bad "$t — $($SSH "grep -m2 -E '^\s+--- FAIL|FAIL' /tmp/$t.out" </dev/null | tr '\n' ' ')"
    fi
  done
fi

CASE=03; if wanted "$@"; then
say "$CASE  the daemon starts on the LIVE config and changes nothing"
  log=$REMOTE/dryrun.log
  $SSH "cd /tmp && ($REMOTE/lotsmand -config /etc/lotsman/r5s.yaml \
        -clash-base http://127.0.0.1:9090 -probe-proxy 127.0.0.1:7891 \
        -interval 30s -check-interval 15m -flowseal-update=false \
        -kb-file /tmp/lt-h/kb.json -state-file /tmp/lt-h/state.json \
        > $log 2>&1 & echo \$! > /tmp/lt-h.pid); sleep 25; kill -9 \$(cat /tmp/lt-h.pid) 2>/dev/null" </dev/null
  errs=$($SSH "grep -c 'level=ERROR' $log" </dev/null || echo 0)
  [ "$errs" = 0 ] && ok "no errors" || bad "$errs error lines — see $log"
  $SSH "grep -q 'recipe pool refreshed' $log" </dev/null \
    && ok "read the installed bundle: $($SSH "grep -o 'bundle_read=[0-9]* added=[0-9]*' $log | head -1" </dev/null)" \
    || meh "no bundle read (is -zapret-compose off?)"
fi

CASE=04; if wanted "$@"; then
say "$CASE  a bundle update carries our state forward"
  # Entirely in /tmp: a fake old bundle with a -user file, a fake new one without.
  res=$($SSH "set -e
    B=/tmp/lt-bundle; rm -rf \$B; mkdir -p \$B/old/lists \$B/new/lists
    echo '10.0.0.0/8' > \$B/old/lists/ipset-exclude-user.txt
    echo 'shipped-old' > \$B/old/lists/list-general.txt
    echo 'shipped-new' > \$B/new/lists/list-general.txt
    ln -sfn \$B/old \$B/current
    echo staged" </dev/null)
  [ "$res" = staged ] && ok "fixture staged (the real check is in 02/flowseal.test)" \
                      || bad "could not stage the fixture"
fi

CASE=05; if wanted "$@"; then
say "$CASE  the engine still starts on the strategy in service"
  # The bundle canary's baseline, run by hand: production argv, spare queue.
  script=$($SSH 'readlink -f /opt/zapret-lotsman/active.sh' </dev/null)
  if [ -z "$script" ]; then meh "no active strategy script"; else
    if $SSH "sh $script $SPARE_QNUM >/tmp/lt-canary.log 2>&1 & P=\$!; sleep 3; \
             if kill -0 \$P 2>/dev/null; then pkill -f 'qnum=$SPARE_QNUM'; kill \$P 2>/dev/null; exit 0; \
             else exit 1; fi" </dev/null; then
      ok "$(basename "$script") starts (queue $SPARE_QNUM, no traffic diverted)"
    else
      bad "$(basename "$script") will not start: $($SSH 'tail -2 /tmp/lt-canary.log' </dev/null | tr '\n' ' ')"
    fi
  fi
fi

CASE=06; if wanted "$@"; then
say "$CASE  a strategy the engine refuses rolls back"
  # A deliberately broken strategy: same script, one payload path made absent.
  if $SSH "test -f /opt/zapret-lotsman/alt12.sh" </dev/null; then
    $SSH "sed 's#\\\$B/#/nonexistent-payload-dir/#g' /opt/zapret-lotsman/alt12.sh > /tmp/lt-bad.sh; chmod +x /tmp/lt-bad.sh" </dev/null
    if $SSH "sh /tmp/lt-bad.sh $SPARE_QNUM >/tmp/lt-bad.log 2>&1 & P=\$!; sleep 3; \
             if kill -0 \$P 2>/dev/null; then pkill -f 'qnum=$SPARE_QNUM'; kill \$P 2>/dev/null; exit 0; else exit 1; fi" </dev/null; then
      bad "the deliberately broken strategy STARTED — the fixture is wrong, not the code"
    else
      ok "a broken strategy is detectable by liveness: $($SSH 'tail -1 /tmp/lt-bad.log' </dev/null | cut -c1-60)"
    fi
  else
    meh "no alt12.sh to derive a broken strategy from"
  fi
fi

CASE=07; if wanted "$@"; then
say "$CASE  the tuner sandbox is valid nft on THIS kernel"
  # Rendering it is a unit test; whether this kernel accepts it is not, and a
  # sandbox that will not load is the difference between measuring a candidate
  # strategy and not having a tuner at all.
  $SSH "cat > /tmp/lt-tune.nft" </dev/null <<'NFT'
table inet lotsman_tune {
    chain post {
        type filter hook postrouting priority -200; policy accept;
        meta mark != 0x4554 return
        oifname "eth0" meta l4proto tcp tcp dport { 80, 443 } ct original packets 1-12 queue num 201 bypass
    }
}
NFT
  if $SSH 'nft -c -f /tmp/lt-tune.nft' </dev/null 2>/tmp/lt-nft.err; then
    ok "the sandbox table passes nft -c"
  else
    bad "sandbox table rejected: $(head -1 /tmp/lt-nft.err)"
  fi
  # The other half: without a skip rule in the LIVE table the probe is desynced
  # twice and measures neither strategy. This is the check that says whether the
  # box is ready for a tuner at all.
  if $SSH 'nft list ruleset 2>/dev/null | grep -q "meta mark 0x4554 return"' </dev/null; then
    ok "the live table lets the sandbox mark through"
  else
    meh "the live table has no sandbox skip rule yet — regenerate /etc/init.d/nfqws before arming a tuner"
  fi
fi

CASE=08; if wanted "$@"; then
say "$CASE  production is exactly as we found it"
  $SSH 'ps w | grep -E "lotsmand|sing-box run|nfqws" | grep -v grep | grep -v lotsman-live' </dev/null \
    | awk '{printf "   %s %s\n", $1, $5}'
  for p in lotsmand sing-box nfqws; do
    $SSH "ps w | grep -v grep | grep -v lotsman-live | grep -q '$p'" </dev/null \
      && ok "$p alive" || bad "$p NOT running"
  done
  left=$($SSH "pgrep -f 'qnum=$SPARE_QNUM' | wc -l" </dev/null)
  [ "$left" = 0 ] && ok "no canary left behind on queue $SPARE_QNUM" || bad "$left stray canary process(es)"
fi

printf '\n\033[1m%d passed, %d failed, %d skipped\033[0m\n' "$pass" "$fail" "$skip"
[ "$fail" = 0 ]
