# Router deployment example

This is what a deployment looks like on disk: `configuration.yaml` plus one file per
rule under `rules/`.

Read it as a pair of halves. The document says **where things come from** —
subscriptions, rule-sets, hosts, pools, engines. A rule file says **what one service
does**: what it matches, how it is probed, and the ladder it climbs when a rung stops
working. Splitting them is what lets a rule be edited, reviewed and changed on its own.

Start from here, or let the tool write one for you:

    lotsman -init -recommended -config /etc/lotsman      # the curated default
    lotsman -init -config /etc/lotsman -sub <url>        # the minimal one

Either way, check before deploying:

    lotsmand -preflight /etc/lotsman    # references, lists, coherence — no side effects
    lotsmanctl generate -config /etc/lotsman -out /tmp/sb.json
    sing-box check -c /tmp/sb.json

`preflight` is the one that fetches: it materialises the rule-sets the document names
into a scratch directory and checks the rules against them, then deletes the scratch. A
document that names a `.srs` nothing provides cannot be checked without that step, which
is why `sing-box check` on the generated output alone is not enough — it resolves
rule-sets lazily and will not tell you the file is missing.

## Placeholders

Every URL, host and password here is a placeholder. Real ones belong in the deployment
on the machine and nowhere else.
