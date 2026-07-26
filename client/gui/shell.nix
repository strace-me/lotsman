# Build environment for the Lotsman GUI (Wails v2 + Svelte) and the tray, on NixOS.
# It provides the toolchain + the webkit/gtk/appindicator deps Wails and the systray
# need at build time. No system rebuild required — this is an ephemeral dev shell.
#
# Channel systems:   nix-shell client/gui/shell.nix
# Flake systems (this ThinkPad), if <nixpkgs> is unset, use the one-liner instead:
#   nix shell nixpkgs#go nixpkgs#nodejs_22 nixpkgs#wails nixpkgs#pkg-config \
#             nixpkgs#gtk3 nixpkgs#webkitgtk_4_1 nixpkgs#libayatana-appindicator
#
# Then build:
#   (cd client/gui/frontend && npm install)
#   (cd client/gui && wails build)             # -> client/gui/build/bin
#   (cd client/tray && go build -o /tmp/lotsman-tray .)
{ pkgs ? import <nixpkgs> { } }:

pkgs.mkShell {
  packages = with pkgs; [
    go
    nodejs_22
    wails
    pkg-config
    gtk3
    webkitgtk_4_1 # Wails v2 links webkit2gtk-4.1
    libayatana-appindicator # fyne.io/systray tray icon
  ];

  # Make sure pkg-config finds webkit2gtk-4.1 + gtk3 (wails doctor checks these).
  shellHook = ''
    export PKG_CONFIG_PATH="${pkgs.webkitgtk_4_1.dev}/lib/pkgconfig:${pkgs.gtk3.dev}/lib/pkgconfig:''${PKG_CONFIG_PATH:-}"
    echo "lotsman gui build shell — run: (cd frontend && npm install) then: wails build"
  '';
}
