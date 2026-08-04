#!/bin/sh
# Flowseal ALT11 -> nfqws (Linux). Source: zapret-discord-youtube 1.9.9a.
# winws-only --wf-tcp/--wf-udp dropped (replaced by nft NFQUEUE); 2 game-filter
# profiles dropped. Usage: alt11.sh [qnum].
QNUM="${1:-200}"
N=/opt/zapret/binaries/linux-arm64/nfqws
L=/opt/flowseal-current/lists
B=/opt/flowseal-current/bin
exec "$N" --qnum="$QNUM" \
--filter-udp=443 --hostlist="$L/list-general.txt" --hostlist="$L/list-general-user.txt" --hostlist-exclude="$L/list-exclude.txt" --hostlist-exclude="$L/list-exclude-user.txt" --ipset-exclude="$L/ipset-exclude.txt" --ipset-exclude="$L/ipset-exclude-user.txt" --dpi-desync=fake --dpi-desync-repeats=11 --dpi-desync-fake-quic="$B/quic_initial_www_google_com.bin" --new \
--filter-udp=19294-19344,50000-50100 --filter-l7=discord,stun --dpi-desync=fake --dpi-desync-fake-discord="$B/quic_initial_dbankcloud_ru.bin" --dpi-desync-fake-stun="$B/quic_initial_dbankcloud_ru.bin" --dpi-desync-repeats=6 --new \
--filter-tcp=2053,2083,2087,2096,8443 --hostlist-domains=discord.media --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern="$B/tls_clienthello_www_google_com.bin" --dpi-desync-fake-tls="$B/tls_clienthello_www_google_com.bin" --new \
--filter-tcp=443 --hostlist="$L/list-google.txt" --ip-id=zero --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=681 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern="$B/tls_clienthello_www_google_com.bin" --dpi-desync-fake-tls="$B/tls_clienthello_www_google_com.bin" --new \
--filter-tcp=80,443 --hostlist="$L/list-general.txt" --hostlist="$L/list-general-user.txt" --hostlist-exclude="$L/list-exclude.txt" --hostlist-exclude="$L/list-exclude-user.txt" --ipset-exclude="$L/ipset-exclude.txt" --ipset-exclude="$L/ipset-exclude-user.txt" --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=664 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern="$B/tls_clienthello_max_ru.bin" --dpi-desync-fake-tls="$B/stun.bin" --dpi-desync-fake-tls="$B/tls_clienthello_max_ru.bin" --dpi-desync-fake-http="$B/tls_clienthello_max_ru.bin" --new \
--filter-udp=443 --ipset="$L/ipset-all.txt" --hostlist-exclude="$L/list-exclude.txt" --hostlist-exclude="$L/list-exclude-user.txt" --ipset-exclude="$L/ipset-exclude.txt" --ipset-exclude="$L/ipset-exclude-user.txt" --dpi-desync=fake --dpi-desync-repeats=11 --dpi-desync-fake-quic="$B/quic_initial_www_google_com.bin" --new \
--filter-tcp=80,443,8443 --ipset="$L/ipset-all.txt" --hostlist-exclude="$L/list-exclude.txt" --hostlist-exclude="$L/list-exclude-user.txt" --ipset-exclude="$L/ipset-exclude.txt" --ipset-exclude="$L/ipset-exclude-user.txt" --dpi-desync=fake,multisplit --dpi-desync-split-seqovl=664 --dpi-desync-split-pos=1 --dpi-desync-fooling=ts --dpi-desync-repeats=8 --dpi-desync-split-seqovl-pattern="$B/tls_clienthello_max_ru.bin" --dpi-desync-fake-tls="$B/stun.bin" --dpi-desync-fake-tls="$B/tls_clienthello_max_ru.bin" --dpi-desync-fake-http="$B/tls_clienthello_max_ru.bin"
