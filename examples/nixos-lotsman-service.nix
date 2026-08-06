# Lotsman as a DAILY DRIVER on NixOS: a root service that owns the tun and the desync
# itself, in place of a system sing-box. Two sing-box instances cannot own one tun, so
# the system daemon stays installed — its package and the firewall settings that make a
# tun work at all are still wanted — but it no longer starts on its own.
#
# NixOS notes that cost time to learn:
#   * /etc/systemd/system is a READ-ONLY symlink farm into the store, so a unit cannot
#     be dropped in by hand. It has to be declared, which is what this module does.
#   * A service declared in another module cannot be turned off with `systemctl disable`
#     — the next rebuild brings it back. Override its wantedBy instead (below).
#   * sing-box.nix carries `networking.firewall.checkReversePath = false`, which the tun
#     needs: with reverse-path filtering on, the asymmetric return traffic is dropped
#     silently. Do not drop that module while chasing the system daemon.
#
# The BINARY is deliberately not in the store: it is rebuilt from source many times a
# day, and wrapping every rebuild in nixos-rebuild is misery. Install it by hand to
# /var/lib/lotsman-bin/. The CONFIG is a plain file in /etc/lotsman/ because it holds a
# subscription URL and node credentials, which have no business in a world-readable
# store — the same reasoning sing-box.nix already applies to its own config.
#
# Reversible: drop ./nixos-lotsman-service.nix from imports and rebuild; the system
# sing-box comes back by itself.
{ config, lib, pkgs, ... }:

let
  # Deliberately NOT under /var/lib/lotsman: that is the service's StateDirectory and
  # systemd chmods it to 0700 (it holds singbox.json with node credentials). The GUI and
  # tray are launched BY THE DESKTOP USER, so their binaries must live somewhere that
  # user can traverse — putting them in the state dir makes the app launcher stop
  # working the moment the service first starts.
  binDir = "/var/lib/lotsman-bin";
  bin = "${binDir}/lotsman-client";

  # Fake payloads for the desync. NOT the store path directly: nixpkgs' zapret ships
  # only upstream's payloads, and 11 of the 67 catalog recipes need Flowseal-derived
  # ones it does not carry (tls_clienthello_4pda_to.bin, tls_clienthello_max_ru.bin,
  # quic_initial_dbankcloud_ru.bin). Pointing at the store therefore silently removes a
  # sixth of the strategies the brain can try — not fatal, since the recipe that beat
  # live TSPU here needs only a payload upstream does ship, but it narrows the search
  # exactly when the current winner stops working and breadth matters most.
  #
  # So: a directory that holds both. ExecStartPre refreshes the store's copies on every
  # start (they follow the pinned package) and leaves anything else alone. The three
  # missing ones are vendored in the repo — see assets/zapret-payloads/README.md — and
  # are installed there once:
  #   sudo install -m0644 assets/zapret-payloads/*.bin /var/lib/lotsman-payloads/
  payloadDir = "/var/lib/lotsman-payloads";
  storePayloads = "${pkgs.zapret}/usr/share/zapret/files/fake";
  user = "operator"; # the desktop user whose unprivileged GUI/tray drives the service
in
{
  # The privilege seam: the service runs as root, the GUI does not. A 0660 socket owned
  # by this group is the only way an unprivileged UI touches it.
  #
  # AFTER THE FIRST REBUILD THE DESKTOP NEEDS MORE THAN A LOGOUT. `systemd --user` is
  # per USER, not per session: it outlives a logout and keeps the group set it was
  # started with, and everything the app launcher starts inherits that. So a session
  # that predates this group will keep hitting "permission denied" on the control
  # socket however many times you log out — measured on this machine. Run
  # `loginctl terminate-user <you>` from a text console, or reboot.
  users.groups.lotsman = { };
  users.users.${user}.extraGroups = [ "lotsman" ];

  # Keep the system sing-box installed, just stop it starting itself.
  systemd.services.sing-box.wantedBy = lib.mkForce [ ];

  # nfqws re-reads its hostlists AFTER dropping privileges, so they cannot live under
  # the 0700 state directory (which also holds singbox.json with node credentials).
  systemd.tmpfiles.rules = [
    "d /var/lib/lotsman-hostlists 0755 root root -"
    # Traversable by the desktop user: the GUI and tray are launched from the app
    # launcher, not by the service.
    "d ${binDir} 0755 root root -"
    # 0755 because nfqws re-reads these AFTER dropping privileges.
    "d ${payloadDir} 0755 root root -"
  ];

  systemd.services.lotsman-client = {
    description = "Lotsman — self-healing censorship bypass (tun + desync)";
    wantedBy = [ "multi-user.target" ];
    after = [ "network-online.target" ];
    wants = [ "network-online.target" ];
    conflicts = [ "sing-box.service" ];
    # Do not give up after a burst of restarts while the network is still coming up.
    startLimitIntervalSec = 0;
    # sing-box runs as a managed child; nft/ip are what the desync installs its rules with.
    path = [ pkgs.sing-box pkgs.zapret pkgs.nftables pkgs.iproute2 ];

    serviceConfig = {
      # Keep the store's payloads current without clobbering the extra ones: -u copies
      # only what is newer, and nothing here removes files.
      ExecStartPre = "${pkgs.coreutils}/bin/cp -ru ${storePayloads}/. ${payloadDir}/";
      ExecStart = lib.concatStringsSep " " [
        bin
        "-config /etc/lotsman/client.yaml"
        "-singbox-bin ${pkgs.sing-box}/bin/sing-box"
        "-nfqws-bin ${pkgs.zapret}/bin/nfqws"
        "-zapret-files ${payloadDir}"
        "-singbox-config /var/lib/lotsman/singbox.json"
        "-state-file /var/lib/lotsman/state.json"
        "-kb-dir /var/lib/lotsman/kb"
        "-ruleset-dir /var/lib/lotsman/rule-sets"
        "-hostlist-dir /var/lib/lotsman-hostlists"
        "-control-socket /run/lotsman/control.sock"
        "-control-socket-group lotsman"
        # -host-dns is deliberately OFF for a first daily-drive. The tun tract alone is
        # proven and its worst failure is "no VPN"; host-DNS rewrites /etc/resolv.conf,
        # and until the roam path re-captures the resolver, moving between networks can
        # leave the machine resolving nothing while every health signal still reads
        # green. Turn it on once the re-capture lands.
        # "-host-dns"
        "-metrics-addr 127.0.0.1:9091"
        "-refresh-interval 5m"
        # -wan is deliberately unset: autodetection survives docking and network changes.
      ];
      Group = "lotsman";
      RuntimeDirectory = "lotsman";
      RuntimeDirectoryMode = "0750"; # the group must traverse to the socket
      # Keep /run/lotsman across a restart: it holds the crash-safe copy of the host's
      # original resolv.conf. Letting systemd wipe it on every restart means an unclean
      # death loses the only record of what DNS looked like before we redirected it —
      # and then nothing can ever put it back.
      RuntimeDirectoryPreserve = "restart";
      StateDirectory = "lotsman";
      StateDirectoryMode = "0700"; # singbox.json carries node credentials
      # on-failure, NOT always: a clean stop is a DECISION. The tray's "off" and
      # `systemctl stop` both end in a normal exit, and Restart=always would undo them
      # ten seconds later — a stop button that does not stop. Every genuine failure
      # still exits non-zero (a refused config, a start that could not reach the
      # subscription, a panic), so those are still retried, including the boot case
      # where the network is not up yet.
      Restart = "on-failure";
      RestartSec = "10s";
    };
  };

  # Let group members start/stop the unit without a password — otherwise a button in the
  # GUI hits an authentication prompt it has nobody to show.
  security.polkit.extraConfig = ''
    polkit.addRule(function(action, subject) {
      if (action.id == "org.freedesktop.systemd1.manage-units" &&
          action.lookup("unit") == "lotsman-client.service" &&
          subject.isInGroup("lotsman")) {
        return polkit.Result.YES;
      }
    });
  '';
}
