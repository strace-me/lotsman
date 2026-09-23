# Branding — Lotsman desktop client

Mark: a **sextant** — the instrument a harbour pilot (лоцман) uses to find their
bearing, i.e. Lotsman finds the working path past the DPI. Chosen after a long
bake-off (buoy / compass / створ / AI-gen attempts all rejected).

- **`lotsman.svg`** — source of truth. Edit this, then re-render the PNGs.
- **`icon-{16,32,64,128,256,512,1024}.png`** — rendered app-icon set (teal tile,
  cream mark).

Re-render (macOS built-in, no deps):

```sh
for s in 16 32 64 128 256 512 1024; do
  qlmanage -t -s "$s" -o . lotsman.svg && mv -f lotsman.svg.png "icon-${s}.png"
done
```

**Tray glyph — `tray.svg`.** Simplified monochrome mark (open wedge + limb arc +
telescope + drum, no small parts), authored with `currentColor` so the app
recolours it per state (neutral / amber warning / red off). `tray-{16,24,32,64}.png`
are mono reference renders. The rich `icon-*.png` above muddies below ~48 px — use
`tray.svg` for the systray.
