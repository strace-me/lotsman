#!/usr/bin/env bash
# youtube-real.sh — измеряет, ГРУЗИТСЯ ЛИ ВИДЕО, а не отвечает ли сайт.
#
# Всё, чем Лоцман до сих пор проверял ютуб, меряет оболочку: проба берёт 204 без
# тела, замер объёма берёт главную страницу. Путь может отдавать 860 КБ HTML и
# вставать на видео — и это не гипотеза, а ровно сигнатура заморозки ТСПУ.
# Поток идёт с *.googlevideo.com, куда не смотрит ни одна наша проба.
#
# Скрипт ничего не меняет: только читает и качает. Запускать МОЖНО при живом
# Лоцмане — он для того и написан, чтобы мерить то, что видит пользователь.
#
#   ./youtube-real.sh            # рикролл, 2 МБ потока
#   VIDEO=<id> MB=8 ./youtube-real.sh
set -u

VIDEO=${VIDEO:-dQw4w9WgXcQ}     # рикролл: живёт вечно, известен всем
MB=${MB:-2}
BYTES=$(( MB * 1024 * 1024 ))
CURL=(curl -sS --no-keepalive --max-time 40)

say() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
row() { printf '  %-34s %s\n' "$1" "$2"; }

say "A. что об этом думает Лоцман"
if S=$(curl -s --max-time 4 --unix-socket /run/lotsman/control.sock http://localhost/status 2>/dev/null); then
  echo "$S" | python3 -c '
import json,sys
d=json.load(sys.stdin)
print("  версия      ", d.get("version"))
for s in d.get("services",[]):
    if s.get("service")=="youtube":
        print("  ступень     ", s.get("rung"), s.get("state"), s.get("rungClass"))
        print("  движок      ", s.get("engine"), s.get("strategy"), s.get("strategyPreset") or "")
        if s.get("requested"): print("  ЗАПРОШЕНО   ", s["requested"], "(расходится с применённым)")
        print("  отказов     ", s.get("fails"), " замерзших", s.get("stalledRatio"))
' 2>/dev/null || echo "  (статус не разобрался)"
else
  row "control.sock" "недоступен — Лоцман не запущен?"
fi

say "B. оболочка — www.youtube.com"
for i in 1 2; do
  printf '  %d: ' "$i"
  "${CURL[@]}" -o /dev/null -w 'код %{http_code}  %{size_download} байт  %{time_total}s  (рукопожатие %{time_appconnect}s)\n' \
    https://www.youtube.com/ || echo ОТКАЗ
done

say "C. CDN картинок — i.ytimg.com (без подписи, стабильный объект)"
printf '  превью: '
"${CURL[@]}" -o /dev/null -w 'код %{http_code}  %{size_download} байт  %{time_total}s\n' \
  "https://i.ytimg.com/vi/$VIDEO/maxresdefault.jpg" || echo ОТКАЗ

say "D. САМ ВИДЕОПОТОК — googlevideo.com, ${MB} МБ"
# Ссылка на поток подписана и живёт минуты, захардкодить нельзя — её надо
# разрешить. yt-dlp делает ровно это; без него честнее сказать, что не смогли,
# чем измерить что-то другое и назвать это видео.
if command -v yt-dlp >/dev/null 2>&1; then
  URL=$(yt-dlp -f 'bestvideo[ext=mp4]/best' --get-url "https://www.youtube.com/watch?v=$VIDEO" 2>/dev/null | head -1)
  if [ -n "$URL" ]; then
    row "хост потока" "$(printf '%s' "$URL" | sed -E 's#https://([^/]+)/.*#\1#')"
    echo "  профиль нарастания — «началось и встало» видно только так:"
    # Одна средняя скорость маскирует именно ту поломку, которую мы ищем: первые
    # килобайты всегда проходят, а встаёт поток дальше. Берём три куска разного
    # размера и сравниваем скорости. Падает с ростом куска — это заморозка.
    #
    # --speed-limit/--speed-time обрывают намертво вставший поток через 10 с
    # вместо ожидания общего таймаута, и тогда видно, СКОЛЬКО успело пройти до
    # остановки — то самое число, ради которого всё и затевалось.
    for chunk in 262144 1048576 "$BYTES"; do
      printf '    %-8s ' "$(( chunk / 1024 ))KB:"
      "${CURL[@]}" --speed-limit 1024 --speed-time 10 -r "0-$((chunk-1))" -o /dev/null \
        -w 'код %{http_code}  %{size_download} байт  %{time_total}s  %{speed_download} Б/с\n' \
        "$URL" || echo "ОБОРВАЛОСЬ (поток встал)"
    done

    # ТОТ ЖЕ поток по QUIC. Оба транспорта несут видео: браузер предпочитает
    # HTTP/3, а при недоступном UDP молча откатывается на TCP и ролик играет.
    # Поэтому «QUIC мёртв» — это не «видео не грузится», а «видео идёт запасным
    # путём»: дольше старт, чаще ребуферинг. Разница между двумя строками ниже и
    # есть цена этого отката, и без неё мы будем чинить не то.
    printf '    %-8s ' "по QUIC:"
    if "${CURL[@]}" --http3 --speed-limit 1024 --speed-time 10 -r "0-$((BYTES-1))" -o /dev/null \
      -w 'код %{http_code}  %{size_download} байт  %{time_total}s  %{speed_download} Б/с\n' \
      "$URL" 2>/dev/null; then :; else
      echo "недоступен (curl без HTTP/3, либо udp/443 к googlevideo закрыт)"
    fi
  else
    row "yt-dlp" "не смог разрешить ссылку (ютуб мог поменять плеер)"
  fi
else
  row "yt-dlp" "НЕ УСТАНОВЛЕН — это единственная часть, меряющая настоящее видео"
  echo "     nix-shell -p yt-dlp   или   nix run nixpkgs#yt-dlp -- ..."
fi

say "E. QUIC / HTTP3 — то, чем ютуб на самом деле ходит"
if "${CURL[@]}" --http3 -o /dev/null -w '' https://www.youtube.com/ 2>/dev/null; then
  printf '  http3:  '
  "${CURL[@]}" --http3 -o /dev/null -w 'код %{http_code}  %{size_download} байт  %{time_total}s\n' \
    https://www.youtube.com/
else
  row "curl --http3" "недоступен или не собран с HTTP/3"
  row "что это значит" "видео пойдёт по TCP — браузер откатывается сам, ролик играет"
  row "и чего это стоит" "дольше старт, чаще ребуферинг; наша проба по TCP этого не увидит вовсе"
fi

say "F. соседи: другие заблокированные, и НА ЧЁМ они сидят"
# Раньше эта секция была подписана «БЕЗ десинка» и это было неправдой: и
# instagram, и x на боевой машине сидят на десинк-ступенях, так что контролем в
# задуманном смысле она никогда не была. Хуже — она приглашала прочитать «у них
# тоже хорошо, значит путь не фильтруется», хотя у них просто СВОЙ рецепт.
#
# Поэтому теперь она не притворяется контролем. Она печатает, на какой ступени и
# на каком рецепте сидит каждый сосед, и результат читается рядом с этим. Разные
# рецепты, разные исходы — это про рецепты; одинаково хорошо у всех, включая
# заведомо заблокированное, — вот тогда путь не фильтруется.
rung_of() {
  curl -s --max-time 4 --unix-socket /run/lotsman/control.sock http://localhost/status 2>/dev/null |
    python3 -c "
import json,sys
try: d=json.load(sys.stdin)
except Exception: sys.exit()
for s in d.get('services',[]):
    if s.get('service')=='$1':
        print('rung%s %s %s' % (s.get('rung'), s.get('rungClass'), s.get('strategy') or ''))
" 2>/dev/null
}
for pair in "instagram https://www.instagram.com/" "x https://x.com/"; do
  set -- $pair
  printf '  %-10s %-34s ' "$1" "$(rung_of "$1")"
  # --speed-limit: заморозка по объёму выглядит как «отдал крохи и встал», и без
  # обрыва по простою она пряталась бы за общим таймаутом ещё полминуты.
  "${CURL[@]}" --speed-limit 1024 --speed-time 10 -o /dev/null \
    -w 'код %{http_code}  %{size_download} байт  %{time_total}s\n' "$2" \
    || echo "ОБОРВАЛОСЬ — отдало крохи и встало (сигнатура заморозки)"
done

say "G. одна сеть, РАЗНЫЕ КЛИЕНТЫ — тот же URL, то же мгновение"
# Смещения сплита позиционные, значит рецепт настроен под определённую ФОРМУ
# TLS-приветствия. Измерено на чистой сети: curl отдал 1.33 МБ, python-urllib
# 1.28 МБ, а requests/urllib3 внутри yt-dlp повис на 45 с три раза из трёх — на
# одном и том же URL в одну и ту же минуту. «Рецепт работает» — утверждение про
# КЛИЕНТА, а не только про сеть, и все наши пробы говорят на одном Go-шном
# диалекте, то есть меряют одну форму из многих.
U="https://www.youtube.com/watch?v=$VIDEO"
printf '  %-24s ' "curl:"
"${CURL[@]}" -o /dev/null -w 'код %{http_code}  %{size_download} байт  %{time_total}s\n' "$U" || echo ОТКАЗ
printf '  %-24s ' "python urllib:"
python3 - "$U" <<'PY' 2>/dev/null || echo "ОТКАЗ или таймаут"
import sys,time,urllib.request
t=time.time()
try:
    n=len(urllib.request.urlopen(sys.argv[1],timeout=25).read())
    print("байт %d  %.2fs" % (n, time.time()-t))
except Exception as e:
    print("ОТКАЗ: %s" % type(e).__name__)
PY
printf '  %-24s ' "python requests:"
python3 - "$U" <<'PY' 2>/dev/null || echo "нет requests — пропущено"
import sys,time
try: import requests
except ImportError: raise SystemExit(1)
t=time.time()
try:
    r=requests.get(sys.argv[1],timeout=25)
    print("байт %d  %.2fs" % (len(r.content), time.time()-t))
except Exception as e:
    print("ОТКАЗ: %s" % type(e).__name__)
PY

cat <<'EOF'

────────────────────────────────────────────────────────────
Как читать:
  D: три скорости примерно равны            → видео реально идёт.
  D: 256KB быстро, а 1MB/4MB втрое медленнее → ПОТОК ВСТАЁТ ПО ДОРОГЕ.
      Это и есть заморозка, и ровно её наши пробы до сих пор не видели:
      «началось» и «идёт» — разные вопросы, и средняя скорость их путает.
  D: «ОБОРВАЛОСЬ» на большом куске           → встало намертво; число байт
      перед этим и есть точка, где путь умирает.
  B и C хорошо, D плохо                     → оболочка грузится, поток нет.
  D по TCP хорошо, по QUIC нет              → видео БУДЕТ играть, но по запасному
      пути: медленнее стартует и чаще ребуферит. Наша TCP-проба этого не видит
      и запишет рецепт как здоровый.
  D хорошо весь, а в браузере рвётся         → дело не в транспорте, а в отличии
      нашего TLS-приветствия от браузерного.
  F: соседи на СВОИХ рецептах, исходы разные → это про рецепты, не про сеть.
  F: все, включая заблокированное, отвечают  → путь не фильтруется вообще,
      и никакой вывод про рецепт из этого запуска не следует.
  G: клиенты расходятся                      → рецепт защищает НЕ ВСЕХ. Смещения
      сплита позиционные, форма приветствия у клиентов разная, и зелёная проба
      удостоверяет только Go-шную форму. Измерено: curl и urllib проходят,
      requests/urllib3 висит 45 с на том же URL в ту же минуту.
────────────────────────────────────────────────────────────
EOF
