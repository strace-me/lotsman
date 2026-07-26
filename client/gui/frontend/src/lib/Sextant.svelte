<script>
  // The app mark — the simplified sextant from client/desktop/assets/tray.svg,
  // inline so it takes currentColor. Used at small sizes (header, empty states).
  export let size = 20
</script>

<svg width={size} height={size} viewBox="0 0 512 512" fill="none" aria-hidden="true">
  <g stroke="currentColor" stroke-width="34" stroke-linecap="round" stroke-linejoin="round">
    <line x1="256" y1="152" x2="91.9" y2="316.1" />
    <line x1="256" y1="152" x2="420.1" y2="316.1" />
    <path d="M 91.9 316.1 A 232 232 0 0 0 420.1 316.1" stroke-width="40" />
    <line x1="304" y1="200" x2="440" y2="188" stroke-width="32" />
  </g>
  <circle cx="256" cy="152" r="16" fill="currentColor" />
  <circle cx="342.9" cy="367.1" r="21" fill="currentColor" />
</svg>
