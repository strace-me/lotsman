#!/usr/bin/env bash
# Run the live checks against a real box.
#
#   test/live/run.sh root@192.168.1.1            # the R5S     (cases 01..08)
#   test/live/run.sh operator@<ip> --client        # the ThinkPad (cases C1..C8)
#   test/live/run.sh root@192.168.1.1 04 06      # only these cases
#
#   SSH_KEY=~/.ssh/id            an identity file, when the agent's default is wrong
#   LOTSMAN_LIVE_SUDO=<password> lets the root cases run; unset, they skip
#   SUSPEND=1 ... --client C6    opt in to the case that really suspends the box
#
# Why this exists: every live validation this project has done was a throwaway
# script written from scratch, run by hand, with the output pasted back — and
# running things has found roughly every bug in this repo, while reading them has
# found roughly none. Suspend/resume went untested for a year because there was
# nowhere to put the case.
#
# HOUSE RULE: a case must not touch production. Anything that needs to mutate
# works in /tmp on the box, and anything that needs an engine binds a spare
# NFQUEUE nothing diverts traffic to. A case that cannot honour that says so and
# skips itself. The client's box is somebody's LAPTOP, so the client cases run
# sing-box in proxy mode and never take over its networking; the two that cannot
# (a namespace, a real suspend) need root and announce themselves.
#
# And a standing warning, earned three times over on the first client run: a case
# that fails is a HARNESS bug until proven otherwise. All three of those were
# quoting or self-matching mistakes in this file, not defects in the code.
set -uo pipefail

cd "$(dirname "$0")/../.."
TARGET=${1:?usage: run.sh user@host [--client] [case...]}
shift || true
# Router cases are numeric (01..08), client cases are C1..C8, and a run does one
# family or the other — the two boxes share no paths.
MODE=router
args=()
for a in "$@"; do
  case "$a" in --client) MODE=client ;; *) args+=("$a") ;; esac
done
set -- ${args+"${args[@]}"}
REMOTE=/tmp/lotsman-live
SPARE_QNUM=${SPARE_QNUM:-299}
# SSH_KEY names an identity file when the box is not reachable with the agent's
# default key. LOTSMAN_LIVE_SUDO carries a sudo password for the cases that need
# root; leave it unset and those cases skip rather than hang on a prompt. It is an
# environment variable on purpose — a password does not belong in this file.
SSH="ssh -o ConnectTimeout=10 -o BatchMode=yes ${SSH_KEY:+-i $SSH_KEY} $TARGET"

pass=0; fail=0; skip=0
say()  { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok()   { printf '   \033[32mPASS\033[0m %s\n' "$*"; pass=$((pass+1)); }
bad()  { printf '   \033[31mFAIL\033[0m %s\n' "$*"; fail=$((fail+1)); }
meh()  { printf '   \033[33mSKIP\033[0m %s\n' "$*"; skip=$((skip+1)); }

wanted() { # no case list = all, within this run's family
  case "$MODE:$CASE" in
    router:C*)      return 1 ;;
    client:[0-9]*)  return 1 ;;
  esac
  [ $# -eq 0 ] && return 0
  for w in "$@"; do [ "$w" = "$CASE" ] && return 0; done
  return 1
}

# root runs the cases that need it, and only if the operator supplied a way. A
# case that cannot get root SKIPS with the reason — a silent fallback to a
# non-root variant would report a pass for a test that never ran.
haveroot() { [ -n "${LOTSMAN_LIVE_SUDO:-}" ] || $SSH 'sudo -n true' </dev/null 2>/dev/null; }
asroot() { # asroot "<shell>"
  if [ -n "${LOTSMAN_LIVE_SUDO:-}" ]; then
    $SSH "sudo -S -p '' sh -c $(printf %q "$1")" <<<"$LOTSMAN_LIVE_SUDO" 2>/dev/null
  else
    $SSH "sudo -n sh -c $(printf %q "$1")" </dev/null
  fi
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

if [ "$MODE" = client ]; then
  GOOS=linux GOARCH=$goarch ./scripts/build.sh client >/dev/null
  # client/ is its own Go module, so its test binaries are compiled from inside it.
  for pkg in ./core ./platform/hostdns ./platform/netid; do
    name=$(basename "$pkg")
    ( cd client && CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go test -c "$pkg" -o "$OLDPWD/dist/$name.test" ) >/dev/null
  done
  CGO_ENABLED=0 GOOS=linux GOARCH=$goarch go test -c ./pkg/config -o dist/config.test >/dev/null
  $SSH "mkdir -p $REMOTE" </dev/null
  for f in dist/lotsman-client dist/core.test dist/hostdns.test dist/netid.test dist/config.test; do
    $SSH "cat > $REMOTE/$(basename "$f") && chmod +x $REMOTE/$(basename "$f")" < "$f"
  done
  # pkg/config's suite parses the repo's own example config through a repo-relative
  # path, and `go test -c` bundles code, not the repo. Recreate just enough tree
  # that ../../examples resolves — the alternative is a FAIL that looks like a
  # config defect, which is how a harness loses its credibility.
  $SSH "mkdir -p $REMOTE/tree/pkg/config $REMOTE/tree/examples" </dev/null
  $SSH "cat > $REMOTE/tree/examples/r5s.yaml" < examples/r5s.yaml
  echo "   staged lotsman-client + 4 test binaries + the example config"
else

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
# Test binaries carry no fixtures: `go test -c` bundles code, not testdata. A
# package whose tests read testdata/ fails on the box for a reason that looks
# like a real defect, which is exactly how a harness loses its credibility.
for d in pkg/strategyimport/testdata; do
  [ -d "$d" ] || continue
  $SSH "mkdir -p $REMOTE/$(basename $(dirname $d))/testdata" </dev/null
  tar cf - -C "$(dirname $d)" testdata | $SSH "tar xf - -C $REMOTE/$(basename $(dirname $d))"
done
echo "   staged $(ls dist/*.test | wc -l | tr -d ' ') test binaries + lotsmand + fixtures"
fi

# busybox has no pkill, and a canary left holding a queue is a real leak on the
# operator's router — so the cleanup has to work with what the box actually has.
$SSH "cat > $REMOTE/killq" </dev/null <<'KILLQ'
#!/bin/sh
# kill whatever holds NFQUEUE $1, without matching this script's own cmdline
me=$$
for p in $(ls /proc | grep '^[0-9]*$'); do
  [ "$p" = "$me" ] && continue
  # The redirect can fail after the process exits, and that error comes from
  # the SHELL, not tr — so the whole group needs the suppression.
  c=$( { tr '\0' ' ' < /proc/$p/cmdline; } 2>/dev/null )
  [ -n "$c" ] || continue   # the process exited while we were reading /proc
  case "$c" in /*nfqws*--qnum=$1*) kill -9 "$p" 2>/dev/null;; esac
done
KILLQ
$SSH "chmod +x $REMOTE/killq" </dev/null

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
    d=$REMOTE; [ "$t" = strategyimport ] && d=$REMOTE/strategyimport
    if $SSH "cd $d && $REMOTE/$t.test -test.count=1 >/tmp/$t.out 2>&1" </dev/null; then
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
        -interval 30s -check-interval 15m -flowseal-update=false -zapret-compose \
        -kb-file /tmp/lt-h/kb.json -state-file /tmp/lt-h/state.json \
        > $log 2>&1 & echo \$! > /tmp/lt-h.pid); sleep 25; kill -9 \$(cat /tmp/lt-h.pid) 2>/dev/null" </dev/null
  errs=$($SSH "grep -c 'level=ERROR' $log || true" </dev/null | head -1)
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
             if kill -0 \$P 2>/dev/null; then $REMOTE/killq $SPARE_QNUM; kill \$P 2>/dev/null; exit 0; \
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
             if kill -0 \$P 2>/dev/null; then $REMOTE/killq $SPARE_QNUM; kill \$P 2>/dev/null; exit 0; else exit 1; fi" </dev/null; then
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
  # nft reads a mark back zero-padded (0x00004554), so comparing our rendered
  # form to its output is a mismatch that looks like a missing rule.
  if $SSH 'nft list ruleset 2>/dev/null | tr -d " " | grep -qi "metamark0x0*4554return"' </dev/null; then
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
  left=$($SSH "cat /proc/net/netfilter/nfnetlink_queue 2>/dev/null | grep -c '^ *$SPARE_QNUM ' || true" </dev/null | head -1)
  [ "$left" = 0 ] && ok "no canary left behind on queue $SPARE_QNUM" || bad "$left stray canary process(es)"
fi

# ----------------------------------------------------------- client cases ----
# The desktop client's box is somebody's LAPTOP, in use. Every case here works in
# /tmp, runs sing-box in PROXY mode (a socks listener — no tun, no routing, no
# resolver touched), and leaves the machine's own tunnel alone. The two that
# cannot honour that (C5 needs a network namespace, C6 actually suspends the
# machine) need root and say what they will do before they do it.
LC=/tmp/lotsman-live-client

CASE=C1; if wanted "$@"; then
say "$CASE  the client knows what it is"
  out=$($SSH "$REMOTE/lotsman-client -config /dev/null 2>&1 | head -2" </dev/null)
  case "$out" in
    *'version="v'*) ok "$(echo "$out" | sed 's/.*version="\([^"]*\)".*/\1/' | head -1)" ;;
    *version=dev*)  bad "reports 'dev' — the build did not stamp a version" ;;
    *)              bad "no version in the startup line: $(echo "$out" | head -1)" ;;
  esac
fi

CASE=C2; if wanted "$@"; then
say "$CASE  pure guarantees, on the box's own filesystem"
  for t in core hostdns netid config; do
    d=/tmp; [ "$t" = config ] && d=$REMOTE/tree/pkg/config
    if $SSH "cd $d && $REMOTE/$t.test -test.count=1 >/tmp/lt-$t.out 2>&1" </dev/null; then
      ok "$t"
    else
      bad "$t — $($SSH "grep -m2 -E '^\s+--- FAIL|^FAIL' /tmp/lt-$t.out" </dev/null | tr '\n' ' ')"
    fi
  done
fi

# stage_client_config writes the scratch config the proxy-mode cases run on. It
# carries a comment on purpose: C4 asserts the toggle does not eat it.
stage_client_config() {
  $SSH "mkdir -p $LC && cat > $LC/config.yaml" </dev/null <<'CFG'
# scratch config for the live harness — nothing here touches the machine
services:
  # the canary
  - name: youtube
    category: streaming
    probe_target: https://www.youtube.com/generate_204
    domains: [youtube.com, googlevideo.com]   # inline comment
  - name: discord
    category: messaging
    probe_target: https://discord.com/api/v9/gateway
    domains: [discord.com]
CFG
}

# client_run starts the client in proxy mode, runs $1 against it, then stops it.
# The body writes its findings to FILES under $LC and the case reads them
# afterwards: the alternative is grepping JSON through three levels of shell
# quoting, and the first version of this harness reported a real feature broken
# because a pattern was mangled on the way, not because anything was wrong.
client_run() {
  local body=$1
  $SSH "bash -s" </dev/null <<REMOTECMD >/dev/null 2>&1
set -uo pipefail
cd $LC
$REMOTE/lotsman-client -config $LC/config.yaml -singbox-config $LC/singbox.json \\
  -proxy 127.0.0.1:11089 -clash 127.0.0.1:19099 -control-socket $LC/ctl.sock \\
  -interval 10s -refresh-interval 0 > $LC/out.log 2>&1 &
PID=\$!
trap 'kill \$PID 2>/dev/null' EXIT
sleep 5
$body
REMOTECMD
}
# grab prints one staged artefact from the box.
grab() { $SSH "cat $LC/$1 2>/dev/null" </dev/null; }

CASE=C3; if wanted "$@"; then
say "$CASE  the client runs in proxy mode and changes nothing"
  before=$($SSH 'systemctl show -p MainPID --value sing-box 2>/dev/null; ip -br link show tun0 2>/dev/null | wc -l' </dev/null | tr '\n' ' ')
  stage_client_config
  client_run "curl -s --unix-socket $LC/ctl.sock http://x/status > $LC/status.json"
  grab status.json | grep -q '\"running\"' && ok "the control socket answers" \
                                           || bad "no status over the socket: $(grab status.json | head -c 120)"
  after=$($SSH 'systemctl show -p MainPID --value sing-box 2>/dev/null; ip -br link show tun0 2>/dev/null | wc -l' </dev/null | tr '\n' ' ')
  [ "$before" = "$after" ] && ok "the machine's own sing-box and tun are untouched ($after)" \
                           || bad "the machine changed under us: $before -> $after"
fi

CASE=C4; if wanted "$@"; then
say "$CASE  a service switches off, and the config keeps its comments"
  stage_client_config
  # Payloads go through a file so no layer of shell has to quote JSON.
  client_run "
    printf '{\"enabled\":false}' > $LC/off.json
    C=\"curl -s -o /dev/null -w %{http_code} --unix-socket $LC/ctl.sock -X POST --data-binary @$LC/off.json\"
    \$C http://x/service/discord/enabled > $LC/r-off
    sleep 3
    curl -s --unix-socket $LC/ctl.sock http://x/status > $LC/status.json
    \$C http://x/service/nosuch/enabled  > $LC/r-unknown
    \$C http://x/service/youtube/enabled > $LC/r-last"
  [ "$(grab r-off)" = 202 ] && ok "switched off (202)" || bad "switch off returned $(grab r-off)"
  grab status.json | grep -q '\"disabled\":\[\"discord\"\]' && ok "reported in /status as disabled" \
    || bad "not reported as disabled: $(grab status.json | grep -o '\"disabled\":[^,}]*')"
  [ "$($SSH "grep -c 'inline comment' $LC/config.yaml" </dev/null)" = 1 ] \
    && ok "the operator's comment survived the edit" || bad "the toggle ate a comment"
  [ "$(grab r-unknown)" = 400 ] && ok "an undeclared service is refused" || bad "unknown service returned $(grab r-unknown)"
  [ "$(grab r-last)" = 400 ] && ok "switching off the last service is refused" || bad "the last service returned $(grab r-last)"
  n=$($SSH "grep -c 'reloaded in place' $LC/out.log || true" </dev/null | head -1)
  [ "${n:-0}" -ge 1 ] && ok "applied in place, no re-exec ($n reload)" || bad "the edit never reached an apply"
fi

CASE=C5; if wanted "$@"; then
say "$CASE  a network change swaps the knowledge base (in a namespace, not on your Wi-Fi)"
  if ! haveroot; then
    meh "needs root: set LOTSMAN_LIVE_SUDO or give the account passwordless sudo"
  else
    # The whole roam happens inside a private netns on a veth pair. netid reads
    # the default gateway's MAC, so changing one static neigh entry IS a roam as
    # far as the client can tell — and the machine's real network never moves.
    stage_client_config
    out=$(asroot "set -x
      ip netns del lotsman-roam 2>/dev/null
      ip link del veth-lr 2>/dev/null
      ip netns add lotsman-roam
      ip link add veth-lr type veth peer name veth-lr-ns
      ip link set veth-lr-ns netns lotsman-roam
      ip addr add 10.77.0.1/24 dev veth-lr; ip link set veth-lr up
      ip netns exec lotsman-roam ip link set lo up
      ip netns exec lotsman-roam ip addr add 10.77.0.2/24 dev veth-lr-ns
      ip netns exec lotsman-roam ip link set veth-lr-ns up
      ip netns exec lotsman-roam ip route add default via 10.77.0.1
      ip netns exec lotsman-roam ip neigh replace 10.77.0.1 lladdr 02:00:00:00:00:aa dev veth-lr-ns nud permanent
      rm -rf $LC/kb; mkdir -p $LC/kb
      ip netns exec lotsman-roam $REMOTE/lotsman-client -config $LC/config.yaml \
        -singbox-config $LC/roam.json -proxy 127.0.0.1:11090 -clash 127.0.0.1:19100 \
        -control-socket $LC/roam.sock -kb-dir $LC/kb -roam-interval 3s \
        -interval 30s -refresh-interval 0 > $LC/roam.log 2>&1 &
      P=\$!
      sleep 8
      ip netns exec lotsman-roam ip neigh replace 10.77.0.1 lladdr 02:00:00:00:00:bb dev veth-lr-ns nud permanent
      sleep 12
      kill \$P 2>/dev/null
      sleep 1
      echo SWAPS=\$(grep -c 'network changed' $LC/roam.log)
      echo STORES=\$(ls $LC/kb | wc -l)
      ip netns del lotsman-roam 2>/dev/null
      ip link del veth-lr 2>/dev/null" 2>/dev/null | grep -E '^(SWAPS|STORES)=')
    swaps=$(echo "$out" | sed -n 's/^SWAPS=//p'); stores=$(echo "$out" | sed -n 's/^STORES=//p')
    [ "${swaps:-0}" -ge 1 ] && ok "the client saw the network move ($swaps swap)" \
                            || bad "no roam detected — the gateway MAC changed and nothing happened"
    [ "${stores:-0}" -ge 2 ] && ok "learning was kept per network ($stores stores)" \
                             || bad "$stores knowledge-base file(s); a roam must open a second"
    $SSH 'ip -br link show veth-lr 2>/dev/null | wc -l' </dev/null | grep -q '^0$' \
      && ok "the namespace and its veth are gone" || bad "the test left an interface behind"
  fi
fi

CASE=C6; if wanted "$@"; then
say "$CASE  the client survives suspend/resume"
  # THE ONE CASE THAT INTERRUPTS THE MACHINE: it really suspends it, for
  # SUSPEND_SECONDS, and relies on the RTC alarm to bring it back. Opt in.
  if [ -z "${SUSPEND:-}" ]; then
    meh "opt-in: SUSPEND=1 test/live/run.sh $TARGET --client C6 — the machine sleeps for ${SUSPEND_SECONDS:-30}s"
  elif ! haveroot; then
    meh "needs root for rtcwake: set LOTSMAN_LIVE_SUDO"
  else
    stage_client_config
    secs=${SUSPEND_SECONDS:-30}
    $SSH "rm -f $LC/suspend.log" </dev/null
    # Detached, because the SSH connection cannot survive the suspend and a
    # foreground run would abandon a live client on a sleeping box.
    asroot "cd $LC && setsid sh -c '
      $REMOTE/lotsman-client -config $LC/config.yaml -singbox-config $LC/susp.json \
        -proxy 127.0.0.1:11091 -clash 127.0.0.1:19101 -control-socket $LC/susp.sock \
        -interval 5s -refresh-interval 0 > $LC/suspend.log 2>&1 &
      echo \$! > $LC/susp.pid
      sleep 5
      date +%s > $LC/susp.before
      rtcwake -m mem -s $secs > $LC/rtcwake.log 2>&1
      date +%s > $LC/susp.after
      sleep 15
      kill \$(cat $LC/susp.pid) 2>/dev/null
      touch $LC/susp.done
    ' >/dev/null 2>&1 &" >/dev/null 2>&1
    echo "   the machine is suspending for ${secs}s — waiting for it to come back"
    sleep $((secs + 35))
    for i in 1 2 3 4 5 6; do $SSH 'test -f '"$LC"'/susp.done' </dev/null 2>/dev/null && break; sleep 10; done
    if ! $SSH "test -f $LC/susp.done" </dev/null 2>/dev/null; then
      bad "the box did not report back — check it by hand before trusting this run"
    else
      slept=$($SSH "echo \$(( \$(cat $LC/susp.after) - \$(cat $LC/susp.before) ))" </dev/null)
      [ "${slept:-0}" -ge $((secs - 5)) ] && ok "the machine really slept (${slept}s)" \
                                          || bad "only ${slept}s elapsed — rtcwake did not suspend, so nothing was tested"
      # The honest assertions: did the process live through it, and did its loop
      # resume? A probe line stamped after the resume is the second one.
      $SSH "grep -q 'shutting down' $LC/suspend.log && echo died || echo lived" </dev/null | grep -q lived \
        && ok "the client was still running after the resume" || bad "the client exited across the suspend"
      after=$($SSH "awk -v t=\$(cat $LC/susp.after) '/msg=probe/{print}' $LC/suspend.log | wc -l" </dev/null)
      [ "${after:-0}" -gt 0 ] && ok "the probe loop resumed ($after probes logged)" \
                              || bad "no probe after the resume — the loop did not restart"
      errs=$($SSH "grep -c level=ERROR $LC/suspend.log || true" </dev/null | head -1)
      [ "${errs:-0}" = 0 ] && ok "no errors across the cycle" || meh "$errs error lines (expected without a subscription)"
    fi
  fi
  # NOT covered here: a tun and the host resolver across a suspend. Proving those
  # means taking over the machine's networking, which this harness will not do.
fi

CASE=C7; if wanted "$@"; then
say "$CASE  the NixOS module evaluates (without activating anything)"
  if ! $SSH 'test -e /etc/nixos/configuration.nix' </dev/null 2>/dev/null; then
    meh "not a NixOS box"
  else
    $SSH "cat > $LC/lotsman-service.nix" </dev/null < examples/nixos-lotsman-service.nix
    # dry-build only: it evaluates the module and builds the unit, and changes
    # nothing about what is running.
    if $SSH "cd $LC && nix-instantiate --parse lotsman-service.nix >/dev/null 2>$LC/nix.err" </dev/null; then
      ok "the module parses on this box's nix"
    else
      bad "the module will not parse: $($SSH "head -2 $LC/nix.err" </dev/null | tr '\n' ' ')"
    fi
    if $SSH "test -f /etc/nixos/lotsman-service.nix" </dev/null 2>/dev/null; then
      $SSH "systemctl cat lotsman-client >/dev/null 2>&1" </dev/null \
        && ok "the unit is installed on the box ($($SSH 'systemctl is-active lotsman-client' </dev/null))" \
        || meh "the module file is in /etc/nixos but the unit is not built — it is not imported"
    else
      meh "the module is not in /etc/nixos yet — activation is still a human step"
    fi
  fi
fi

CASE=C8; if wanted "$@"; then
say "$CASE  the laptop is exactly as we found it"
  # C5 runs as root, so some of its artefacts are root-owned and the unprivileged
  # sweep silently leaves them — which the next run would inherit.
  $SSH "rm -rf $LC" </dev/null 2>/dev/null
  if $SSH "test -e $LC" </dev/null 2>/dev/null; then
    haveroot && asroot "rm -rf $LC" >/dev/null 2>&1
  fi
  $SSH "test -e $LC" </dev/null 2>/dev/null \
    && bad "could not remove $LC — root-owned leftovers from C5" || ok "scratch dir removed"
  for p in "sing-box"; do
    $SSH "systemctl is-active $p >/dev/null 2>&1" </dev/null && ok "$p still active" || meh "$p is not active (was it before?)"
  done
  # -x matches the process NAME, not the cmdline: a -f pattern also matches the
  # ssh command carrying it, and this check reported a stray that was itself.
  # (The opposite trap is nfqws, which rewrites its title so -x misses it — hence
  # killq reads /proc there. Different binary, different rule.)
  left=$($SSH 'pgrep -x lotsman-client | wc -l' </dev/null)
  [ "${left:-0}" = 0 ] && ok "no test process left behind" || bad "$left stray process(es) from this run"
fi

printf '\n\033[1m%d passed, %d failed, %d skipped\033[0m\n' "$pass" "$fail" "$skip"
[ "$fail" = 0 ]
