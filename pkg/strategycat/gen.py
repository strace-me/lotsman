#!/usr/bin/env python3
# gen.py - regenerates catalog.yaml for pkg/strategycat.
#
# Reproducible parse approach (run when upstreams update):
#   1. zms (StressOzz/Zapret-Manager): fetch the raw repo files
#        Zapret-Manager.sh        -> Dv1..Dv17  (var assignments  `DvN=$'...'`)
#                                 -> strategy_Gv1 / strategy_Gv() (QUIC games, --filter-udp)
#                                 -> strategy_TCP_common()        (games TCP)
#        Strategies.md            -> general v1..v9  (each ``` block; may hold several --new sub-blocks)
#        Strategies_For_Youtube.md-> Yv01..Yv27      (each ``` block, one --new block)
#   2. Flowseal (Flowseal/zapret-discord-youtube @ 1.9.9a): fetch the `general*.bat` files.
#        winws args; join lines on `^`, split on `--new`. Each segment = one block.
#   3. Router (root@192.168.1.1): /opt/zapret-lotsman/{active.sh,alt11.sh} - the REAL deployed
#        nfqws blocks (Flowseal ALT11 / ALT12 ported to Linux nfqws). split on `--new`.
#
# Normalization applied to every block before dedup:
#   - file paths (fake bins, hostlists, ipset, split/fake patterns) -> bare basename
#   - `--hostlist-domains=...` value -> "{{DOMAINS}}" placeholder (Lotsman fills per-service);
#     hostlist/ipset FILE refs that select a target set -> dropped (they are deployment-specific),
#     EXCEPT --filter-l7 / --filter-tcp / --filter-udp which carry protocol/port intent.
#   - args kept in source order.
# Dedup key = the tuple of normalized args. First source seen wins the id; others -> provenance note.
#
# techniques tags are DERIVED from args (see derive_tags) so they always match nfqws_args.

import re

# ---- raw blocks: (id, target_class, protocol, provenance, raw_args_multiline, notes) ----
# raw_args use the upstream form; normalization below strips paths to basenames.

RAW = []

def add(id, cls, proto, prov, args, notes=""):
    RAW.append((id, cls, proto, prov, args.strip(), notes))

ZMS_VER = "Zapret-Manager 9.6 (zapret 72.20260307)"
FS_VER = "Flowseal/zapret-discord-youtube 1.9.9a"
RT_VER = "router 192.168.1.1 /opt/zapret-lotsman (Flowseal 1.9.9a ported to nfqws)"

# ---------- zms Dv1..Dv17 : discord.media TCP ----------
DV = {
 1: "--dpi-desync=multisplit --dpi-desync-split-seqovl=652 --dpi-desync-split-pos=2 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 2: "--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 3: "--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=none",
 4: "--dpi-desync=multisplit --dpi-desync-split-seqovl=652 --dpi-desync-split-pos=2 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 5: "--dpi-desync=fake,multisplit --dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=1000 --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 6: "--dpi-desync=multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 7: "--dpi-desync=multisplit --dpi-desync-split-pos=2,sniext+1 --dpi-desync-split-seqovl=679 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 8: "--dpi-desync=fake --dpi-desync-fake-tls-mod=none --dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2",
 9: "--dpi-desync=fake,fakedsplit --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2 --dpi-desync-repeats=8 --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 10:"--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=10000000 --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 11:"--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 12:"--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2 --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 13:"--dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 14:"--dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fakedsplit-pattern=0x00 --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 15:"--dpi-desync=fake,multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=11 --dpi-desync-fooling=badseq --dpi-desync-fake-tls=0x00000000 --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 16:"--dpi-desync=fake,hostfakesplit --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com --dpi-desync-hostfakesplit-mod=host=www.google.com,altorder=1 --dpi-desync-fooling=ts",
 17:"--dpi-desync=hostfakesplit --dpi-desync-repeats=4 --dpi-desync-fooling=ts --dpi-desync-hostfakesplit-mod=host=www.google.com",
}
for n, a in DV.items():
    add(f"zms-dv{n}", "discord_tcp", "tcp", ZMS_VER,
        f"--filter-tcp=2053,2083,2087,2096,8443 --hostlist-domains={{{{DOMAINS}}}} {a}",
        "discord.media voice/media TCP-TLS recipe")

# ---------- zms Yv01..Yv27 : YouTube / google TCP-TLS ----------
YV = {
 "01":"--ip-id=zero --dpi-desync=multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 "02":"--dpi-desync=multisplit --dpi-desync-split-pos=1,sniext+1 --dpi-desync-split-seqovl=1",
 "03":"--dpi-desync=fake,multisplit --dpi-desync-split-pos=2,sld --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=ggpht.com --dpi-desync-split-seqovl=620 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fooling=badsum,badseq",
 "04":"--dpi-desync=split2 --dpi-desync-split-seqovl=681 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
 "05":"--dpi-desync=fake,fakeddisorder --dpi-desync-split-pos=10,midsld --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=fonts.google.com --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls-mod=none --dpi-desync-fakedsplit-pattern=tls_clienthello_vk_com.bin --dpi-desync-split-seqovl=336 --dpi-desync-split-seqovl-pattern=tls_clienthello_gosuslugi_ru.bin --dpi-desync-fooling=badseq,badsum --dpi-desync-badseq-increment=0",
 "06":"--dpi-desync=multidisorder --dpi-desync-split-pos=7,sld+1 --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com --dpi-desync-fooling=badseq --dpi-desync-autottl=2:2-12",
 "07":"--dpi-desync=multidisorder --dpi-desync-split-pos=1,midsld,endhost-1 --dpi-desync-repeats=2 --dpi-desync-fooling=md5sig --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 "08":"--dpi-desync=fake,multisplit --dpi-desync-fake-tls=0x00000000 --dpi-desync-fake-tls=! --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=2 --dpi-desync-fooling=badseq --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 "09":"--dpi-desync-repeats=6 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=2 --dpi-desync=multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-fake-quic=quic_initial_www_google_com.bin",
 "10":"--dpi-desync=multisplit --dpi-desync-split-pos=1,2 --dpi-desync-split-seqovl=4 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com",
 "11":"--dpi-desync=multidisorder --dpi-desync-split-pos=2,5,105,host+5,sld-1,endsld-5,endsld",
 "12":"--dpi-desync=multidisorder --dpi-desync-split-pos=1,midsld --dpi-desync-repeats=2",
 "13":"--dpi-desync=fake,multidisorder --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq --dpi-desync-badseq-increment=10000000 --dpi-desync-repeats=2 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=fonts.google.com",
 "14":"--dpi-desync=fake,multidisorder --dpi-desync-split-pos=10,midsld --dpi-desync-fake-tls=0x00000000 --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=fonts.google.com --dpi-desync-split-seqovl=336 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fooling=badseq",
 "15":"--dpi-desync=fake,multisplit --dpi-desync-split-pos=2,sld --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=ggpht.com --dpi-desync-split-seqovl=2108 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fooling=badsum,badseq",
 "16":"--dpi-desync=multisplit --dpi-desync-split-pos=1,sniext+1 --dpi-desync-split-seqovl=1 --dpi-desync-fooling=badsum,badseq --dpi-desync-badseq-increment=0",
 "17":"--dpi-desync=fakeddisorder --dpi-desync-fooling=md5sig --dup=1 --dup-cutoff=n2 --dup-fooling=md5sig --dpi-desync-split-pos=method+2",
 "18":"--ip-id=zero --dpi-desync=fake,hostfakesplit --dpi-desync-fake-tls-mod=rnd,dupsid,sni=www.google.com --dpi-desync-hostfakesplit-mod=host=www.google.com,altorder=1 --dpi-desync-fooling=ts",
 "19":"--dpi-desync=hostfakesplit --dpi-desync-hostfakesplit-mod=host=google.com --dpi-desync-fooling=ts",
 "20":"--ip-id=zero --dpi-desync=fake,fakedsplit --dpi-desync-repeats=6 --dpi-desync-fooling=ts --dpi-desync-fakedsplit-pattern=0x00 --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 "21":"--ip-id=zero --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
 "22":"--dpi-desync=split2 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl=681 --dpi-desync-split-seqovl-pattern=stun.bin",
 "23":"--dpi-desync=fake,fakeddisorder --dpi-desync-split-pos=1 --dpi-desync-fake-tls=stun.bin --dpi-desync-fake-tls-mod=none --dpi-desync-fakedsplit-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fooling=badseq,badsum --dpi-desync-badseq-increment=0",
 "24":"--dpi-desync=fake,multisplit --dpi-desync-split-seqovl=654 --dpi-desync-split-pos=1 --dpi-desync-fooling=badseq,badsum --dpi-desync-split-seqovl-pattern=stun.bin --dpi-desync-fake-tls=stun.bin --dpi-desync-badseq-increment=0",
 "25":"--dpi-desync=hostfakesplit --dpi-desync-fooling=badseq,badsum --dpi-desync-hostfakesplit-mod=host=mapgl.2gis.com --dpi-desync-badseq-increment=0",
 "26":"--dpi-desync=multidisorder --dpi-desync-split-pos=1,sniext+1,host+1,midsld-2,midsld,midsld+2,endhost-1",
 "27":"--dpi-desync=multidisorder --dpi-desync-split-pos=1,2,3,5,105,host+5,sld-1,endsld-5,endsld --dpi-desync-fooling=badsum",
}
for n, a in YV.items():
    add(f"zms-yv{n}", "youtube", "tcp", ZMS_VER,
        f"--filter-tcp=443 --hostlist-domains={{{{DOMAINS}}}} {a}",
        "YouTube/Google TCP-TLS recipe (orig --hostlist=zapret-hosts-google.txt)")

# ---------- zms games (Gv) QUIC + TCP_common ----------
add("zms-gv-stun", "games", "udp", ZMS_VER,
    "--filter-udp=1024-65535 --dpi-desync=fake --dpi-desync-cutoff=d2 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=stun.bin",
    "games QUIC/UDP, STUN fake (strategy_Gv1)")
add("zms-gv-quic", "games", "udp", ZMS_VER,
    "--filter-udp=1024-65535 --dpi-desync=fake --dpi-desync-repeats=10 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=quic_initial_www_google_com.bin --dpi-desync-cutoff=n{{N}}",
    "games QUIC/UDP, google QUIC fake, cutoff=n<N> (strategy_Gv N)")
add("zms-games-tcp", "games", "tcp", ZMS_VER,
    "--filter-tcp=2802,2302,2502,6112-6119,6695-6710,25565,27015-27030,27036-27037,50001 --dpi-desync-any-protocol=1 --dpi-desync-cutoff=n5 --dpi-desync=multisplit --dpi-desync-split-seqovl=582 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=stun.bin",
    "games TCP common (strategy_TCP_common)")

# ---------- zms general (Strategies.md v1..v9) : distinct sub-blocks ----------
add("zms-general-v1", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=split2 --dpi-desync-split-seqovl=681 --dpi-desync-split-seqovl-pattern=stun.bin",
    "general TLS split2 + seqovl(stun)")
add("zms-general-fakeddisorder", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=fake,fakeddisorder --dpi-desync-split-pos=10,midsld --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls-mod=rnd,dupsid,sni=fonts.google.com --dpi-desync-fake-tls=0x0F0F0F0F --dpi-desync-fake-tls-mod=none --dpi-desync-fakedsplit-pattern=tls_clienthello_vk_com.bin --dpi-desync-split-seqovl=336 --dpi-desync-split-seqovl-pattern=tls_clienthello_gosuslugi_ru.bin --dpi-desync-fooling=badseq,badsum --dpi-desync-badseq-increment=0",
    "general TLS multi-fake fakeddisorder (Strategies.md v2 TCP block)")
add("zms-general-quic", "quic", "udp", ZMS_VER,
    "--filter-udp=443 --dpi-desync=fake --dpi-desync-repeats=4 --dpi-desync-fake-quic=quic_initial_www_google_com.bin",
    "general QUIC fake (Strategies.md v2..v4 UDP block)")
add("zms-general-multisplit-stun", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=multisplit --dpi-desync-split-seqovl=582 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=stun.bin",
    "general TLS multisplit seqovl=582 (Strategies.md v4 block 2)")
add("zms-general-hostfakesplit-2gis", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=hostfakesplit --dpi-desync-hostfakesplit-mod=host=i2.photo.2gis.com --dpi-desync-hostfakesplit-midhost=host-2 --dpi-desync-split-seqovl=726 --dpi-desync-fooling=badsum,badseq --dpi-desync-badseq-increment=0",
    "general TLS hostfakesplit midhost (Strategies.md v6 block 2)")
add("zms-general-hostfakesplit-mapgl", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=hostfakesplit --dpi-desync-fooling=badseq,badsum --dpi-desync-hostfakesplit-mod=host=mapgl.2gis.com --dpi-desync-badseq-increment=0",
    "general TLS hostfakesplit (Strategies.md v9)")
add("zms-general-fake-4pda", "general_tls", "tcp", ZMS_VER,
    "--filter-tcp=443 --dpi-desync=fake --dpi-desync-fooling=ts --dpi-desync-fake-tls=4pda.bin --dpi-desync-fake-tls-mod=none",
    "general TLS plain fake 4pda (Strategies.md v8 block 2)")

# ---------- zms discord (Strategies.md) UDP discord/stun block ----------
add("zms-discord-quic", "quic", "udp", ZMS_VER,
    "--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord=stun.bin --dpi-desync-fake-stun=stun.bin --dpi-desync-repeats=6",
    "Discord voice QUIC/STUN fake (Strategies.md Discord block)")

# ---------- Flowseal distinct block families (from general*.bat, 1.9.9a) ----------
add("flowseal-quic-general", "quic", "udp", FS_VER,
    "--filter-udp=443 --hostlist-domains={{DOMAINS}} --dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fake-quic=quic_initial_www_google_com.bin",
    "general QUIC google fake; ALT11/12 use repeats=11")
add("flowseal-discord-stun", "quic", "udp", FS_VER,
    "--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord=quic_initial_dbankcloud_ru.bin --dpi-desync-fake-stun=quic_initial_dbankcloud_ru.bin --dpi-desync-repeats=6",
    "Discord voice (dbankcloud QUIC fake)")
add("flowseal-discord-tcp-multisplit-681", "discord_tcp", "tcp", FS_VER,
    "--filter-tcp=2053,2083,2087,2096,8443 --hostlist-domains={{DOMAINS}} --dpi-desync=multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
    "discord.media TCP multisplit seqovl=681")
add("flowseal-discord-tcp-multisplit-652", "discord_tcp", "tcp", FS_VER,
    "--filter-tcp=2053,2083,2087,2096,8443 --hostlist-domains={{DOMAINS}} --dpi-desync=multisplit --dpi-desync-split-seqovl=652 --dpi-desync-split-pos=2 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
    "discord.media TCP multisplit seqovl=652 (ALT2)")
add("flowseal-google-multisplit-681", "youtube", "tcp", FS_VER,
    "--filter-tcp=443 --hostlist-domains={{DOMAINS}} --ip-id=zero --dpi-desync=multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin",
    "google/YouTube TCP multisplit (orig --hostlist=list-google.txt)")
add("flowseal-general-multisplit-568-4pda", "general_tls", "tcp", FS_VER,
    "--filter-tcp=80,443 --hostlist-domains={{DOMAINS}} --dpi-desync=multisplit --dpi-desync-split-seqovl=568 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_4pda_to.bin",
    "general HTTP/TLS multisplit seqovl=568 (4pda pattern)")
add("flowseal-general-fake-multisplit-664-max", "general_tls", "tcp", FS_VER,
    "--filter-tcp=80,443 --hostlist-domains={{DOMAINS}} --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=664 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_max_ru.bin --dpi-desync-fake-tls=stun.bin --dpi-desync-fake-tls=tls_clienthello_max_ru.bin --dpi-desync-fake-http=tls_clienthello_max_ru.bin",
    "general fake+multisplit seqovl=664 (ALT11/12 deployed; max.ru pattern)")
add("flowseal-ipset-quic", "quic", "udp", FS_VER,
    "--filter-udp=443 --ipset={{IPSET}} --dpi-desync=fake --dpi-desync-repeats=6 --dpi-desync-fake-quic=quic_initial_www_google_com.bin",
    "ipset-targeted QUIC google fake")
add("flowseal-ipset-tcp-multisplit-568", "general_tls", "tcp", FS_VER,
    "--filter-tcp=80,443,8443 --ipset={{IPSET}} --dpi-desync=multisplit --dpi-desync-split-seqovl=568 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_4pda_to.bin",
    "ipset-targeted TLS multisplit seqovl=568")
add("flowseal-games-tcp-multisplit", "games", "tcp", FS_VER,
    "--filter-tcp={{GAME_PORTS}} --ipset={{IPSET}} --dpi-desync=multisplit --dpi-desync-any-protocol=1 --dpi-desync-cutoff=n3 --dpi-desync-split-seqovl=568 --dpi-desync-split-pos=1 --dpi-desync-split-seqovl-pattern=tls_clienthello_4pda_to.bin",
    "games TCP multisplit any-protocol cutoff=n3")
add("flowseal-games-udp-fake", "games", "udp", FS_VER,
    "--filter-udp={{GAME_PORTS}} --ipset={{IPSET}} --dpi-desync=fake --dpi-desync-repeats=12 --dpi-desync-any-protocol=1 --dpi-desync-fake-unknown-udp=quic_initial_dbankcloud_ru.bin --dpi-desync-cutoff=n2",
    "games UDP fake-unknown any-protocol cutoff=n2")
add("flowseal-syndata", "general_tls", "tcp", FS_VER,
    "--filter-tcp=80,443 --hostlist-domains={{DOMAINS}} --dpi-desync=syndata",
    "syndata payload-in-SYN (SIMPLE FAKE family)")
add("flowseal-syndata-multidisorder", "general_tls", "tcp", FS_VER,
    "--filter-l3=ipv4 --filter-tcp=80,443 --hostlist-domains={{DOMAINS}} --dpi-desync=syndata,multidisorder",
    "syndata + multidisorder (ipv4)")

# ---------- Router: real deployed nfqws blocks (ALT12 active.sh) that differ from above ----------
add("router-discord-tcp-fake-multisplit", "discord_tcp", "tcp", RT_VER,
    "--filter-tcp=2053,2083,2087,2096,8443 --hostlist-domains={{DOMAINS}} --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern=tls_clienthello_www_google_com.bin --dpi-desync-fake-tls=tls_clienthello_www_google_com.bin",
    "DEPLOYED discord.media block (active.sh / alt11.sh)")
add("router-google-hostfakesplit", "youtube", "tcp", RT_VER,
    "--filter-tcp=443 --hostlist-domains={{DOMAINS}} --ip-id=zero --dpi-desync=hostfakesplit --dpi-desync-fooling=ts --dpi-desync-hostfakesplit-mod=host=www.google.com",
    "DEPLOYED google block, ALT12 active.sh (alt11 uses fake,multisplit instead)")
add("router-discord-quic-stun-dbank", "quic", "udp", RT_VER,
    "--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord=stun.bin --dpi-desync-fake-discord=quic_initial_dbankcloud_ru.bin --dpi-desync-fake-stun=quic_initial_dbankcloud_ru.bin --dpi-desync-repeats=3",
    "DEPLOYED discord voice block, active.sh (stun.bin + dbankcloud, repeats=3)")
add("router-quic-google-r11", "quic", "udp", RT_VER,
    "--filter-udp=443 --hostlist-domains={{DOMAINS}} --dpi-desync=fake --dpi-desync-repeats=11 --dpi-desync-fake-quic=quic_initial_www_google_com.bin",
    "DEPLOYED general QUIC block (repeats=11)")

# ============================================================================
# normalization + tag derivation + dedup + emit
# ============================================================================

def derive_tags(args):
    s = " " + " ".join(args) + " "
    tags = []
    m = re.search(r"--dpi-desync=([a-z0-9,]+)", s)
    if m:
        for v in m.group(1).split(","):
            tags.append(v)  # multisplit / fake / fakedsplit / multidisorder / hostfakesplit / syndata / split2 / fakeddisorder
    if "--dpi-desync-split-seqovl=" in s:
        tags.append("seqovl")
    mf = re.search(r"--dpi-desync-fooling=([a-z0-9,]+)", s)
    if mf:
        tags.append("fooling:" + mf.group(1))
    mr = re.search(r"--dpi-desync-repeats=([0-9]+|\{\{N\}\})", s)
    if mr:
        tags.append("repeats:" + mr.group(1))
    mc = re.search(r"--dpi-desync-cutoff=([a-z0-9{}]+)", s)
    if mc:
        tags.append("cutoff:" + mc.group(1))
    if "--dpi-desync-fake-quic=" in s:
        tags.append("fake-quic")
    if "--dpi-desync-fake-tls=" in s:
        tags.append("fake-tls")
    if "--dpi-desync-fake-tls-mod=" in s and "sni=" in s:
        tags.append("fake-sni")
    if "--dpi-desync-fake-unknown-udp=" in s:
        tags.append("fake-unknown-udp")
    if "--filter-l7=" in s:
        tags.append("l7-filter")
    if "--dpi-desync-any-protocol=1" in s:
        tags.append("any-protocol")
    if "--ip-id=zero" in s:
        tags.append("ip-id-zero")
    if "--dup=" in s:
        tags.append("dup")
    # de-dup tags, keep order
    seen = set(); out = []
    for t in tags:
        if t not in seen:
            seen.add(t); out.append(t)
    return out

recipes = []
seen_args = {}
dups = 0
for id, cls, proto, prov, raw, notes in RAW:
    args = raw.split()
    key = tuple(args)
    if key in seen_args:
        # fold provenance into the existing recipe
        existing = seen_args[key]
        if prov not in existing["provenance"]:
            existing["provenance"] = existing["provenance"] + " | also: " + prov + " (" + id + ")"
        dups += 1
        continue
    rec = {
        "id": id,
        "provenance": prov,
        "target_class": cls,
        "protocol": proto,
        "techniques": derive_tags(args),
        "nfqws_args": args,
        "notes": notes,
    }
    seen_args[key] = rec
    recipes.append(rec)

doc = {
    "schema": 1,
    "description": "Curated zapret/nfqws DPI-desync strategy recipes for Lotsman. "
                   "One recipe = one nfqws --new block. Paths normalized to basenames; "
                   "--hostlist-domains / --ipset / game ports templated as {{...}}. "
                   "Regenerate with pkg/strategycat/gen.py (see header for parse approach).",
    "sources": [ZMS_VER, FS_VER, RT_VER],
    "recipes": recipes,
}

def yq(s):
    # quote a YAML scalar safely as a double-quoted string
    return '"' + s.replace("\\", "\\\\").replace('"', '\\"') + '"'

lines = ["# AUTO-GENERATED by gen.py - do not edit by hand. See gen.py header."]
lines.append("schema: 1")
lines.append("description: " + yq(doc["description"]))
lines.append("sources:")
for s in doc["sources"]:
    lines.append("  - " + yq(s))
lines.append("recipes:")
for r in recipes:
    lines.append("  - id: " + yq(r["id"]))
    lines.append("    provenance: " + yq(r["provenance"]))
    lines.append("    target_class: " + yq(r["target_class"]))
    lines.append("    protocol: " + yq(r["protocol"]))
    if r["techniques"]:
        lines.append("    techniques: [" + ", ".join(yq(t) for t in r["techniques"]) + "]")
    else:
        lines.append("    techniques: []")
    lines.append("    nfqws_args:")
    for a in r["nfqws_args"]:
        lines.append("      - " + yq(a))
    lines.append("    notes: " + yq(r["notes"]))

with open("catalog.yaml", "w") as f:
    f.write("\n".join(lines) + "\n")

from collections import Counter
bc = Counter(r["target_class"] for r in recipes)
bp = Counter(r["provenance"].split(" | ")[0] for r in recipes)
print(f"recipes: {len(recipes)}  (folded {dups} duplicates)")
print("by class:", dict(bc))
print("by protocol:", dict(Counter(r["protocol"] for r in recipes)))
print("by source:", dict(bp))
