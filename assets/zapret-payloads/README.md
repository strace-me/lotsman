# Fake payloads nixpkgs does not ship

Three files, a few hundred bytes each. They are recorded packets — a TLS ClientHello and
a QUIC Initial — that nfqws sends as the *fake* half of a desync, so the DPI sees a
plausible handshake to a domain it does not block while the real one slips past.

## Why they are vendored here

`pkgs.zapret` in nixpkgs carries only upstream's payload set. These three come from the
Flowseal bundle (`zapret-discord-youtube`), and **11 of the 67 recipes in
`pkg/strategycat/catalog.yaml` name them**:

| file | recipes |
|---|---|
| `quic_initial_dbankcloud_ru.bin` | 5 |
| `tls_clienthello_4pda_to.bin` | 3 |
| `tls_clienthello_max_ru.bin` | 3 |

Without them the client does not fail — `usablePayloadRecipes` drops a recipe whose
payload is missing — it just quietly has a sixth of its strategies unavailable. That is
the bad kind of missing: it costs nothing until the strategy that currently works stops
working, and then the search that should recover it is narrower than it looks, while the
symptom reads as "the desync isn't finding anything".

They lived on one laptop until 2026-08-04. Committing them makes the desync catalog
reproducible on a fresh machine instead of depending on a directory somebody happened to
keep. The repo is private; these are captured packets rather than code.

## Using them

The desync needs ONE directory holding both these and the packaged ones — nfqws resolves
a recipe's bare filename against its working directory, so a split set does not work.
`examples/nixos-lotsman-service.nix` does this: an `ExecStartPre` refreshes the packaged
payloads into `/var/lib/lotsman-payloads` on every start (so they follow the pinned
package) while leaving everything else alone. Put these three there once:

```bash
sudo install -d -m0755 /var/lib/lotsman-payloads
sudo install -m0644 assets/zapret-payloads/*.bin /var/lib/lotsman-payloads/
```

0644 under a 0755 directory matters: nfqws re-reads its files *after* dropping
privileges, so anything it cannot read as an unprivileged user is a payload it silently
does without.
