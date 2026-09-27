# Extra fake payloads (optional)

`nfqws` sends a *fake* half of a desync: a recorded TLS ClientHello or QUIC Initial
that the DPI sees as a plausible handshake to a domain it does not block, while
the real one slips past. Everything `pkgs.zapret` ships works for most recipes.

Three more are used by the bundled Flowseal recipes, and they are **not** in the
repository:

| file | recipes |
|---|---|
| `quic_initial_dbankcloud_ru.bin` | 5 |
| `tls_clienthello_4pda_to.bin` | 3 |
| `tls_clienthello_max_ru.bin` | 3 |

They are third-party recordings from the Flowseal bundle
(`zapret-discord-youtube`), and this tree is public, so they are not vendored
here. Install them once, from wherever you obtained the bundle, into the payload
directory:

```sh
sudo install -d -m0755 /var/lib/lotsman-payloads
sudo install -m0644 /path/to/*.bin /var/lib/lotsman-payloads/
```

0644 under a 0755 directory matters: `nfqws` re-reads its files *after* dropping
privileges, so anything it cannot read as an unprivileged user is a payload it
silently does without.

## What happens if you skip this

Nothing fails. A recipe whose payload is missing is dropped from the candidate
set, so the search that would recover you is narrower than it looks — while the
symptom reads as "the desync isn't finding anything". A missing payload is
therefore **reported at startup**, by name and count, rather than discovered
later as a strategy that stopped working:

```
desync recipes unavailable on this host (payload files missing) count=11 dir=/var/lib/lotsman-payloads examples=[…]
```

`nfqws` resolves a recipe's bare payload filename against its working directory,
so the packaged and the extra payloads must live in **one** directory. A split
set does not work. `examples/nixos-lotsman-service.nix` shows the arrangement: an
`ExecStartPre` refreshes the packaged payloads into `/var/lib/lotsman-payloads`
on every start, so they follow the pinned package, and your three sit alongside.
