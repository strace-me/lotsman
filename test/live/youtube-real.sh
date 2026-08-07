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

say "F. контроль: заблокированное БЕЗ десинка"
# Если эти отвечают так же бодро, как ютуб, значит десинк ни при чём и путь
# просто не фильтруется — вывод «рецепт работает» был бы ложным.
for u in https://www.instagram.com/ https://x.com/; do
  printf '  %-28s ' "$(printf '%s' "$u" | sed -E 's#https://([^/]+)/.*#\1#')"
  "${CURL[@]}" -o /dev/null -w 'код %{http_code}  %{size_download} байт  %{time_total}s  %{errormsg}\n' "$u" || echo ОТКАЗ
done

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
  F отвечает так же бодро, как ютуб         → путь не фильтруется вообще,
      и никакой вывод про рецепт из этого запуска не следует.
────────────────────────────────────────────────────────────
EOF
