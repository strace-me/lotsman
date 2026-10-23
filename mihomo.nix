# mihomo (Clash.Meta) — чистая служба-фоллбек, без GUI.
#
# Конфиг mutable: /var/lib/mihomo/config.yaml (редактируется руками/webui).
# Служба задизейблена: запускается вручную.
{ config, lib, pkgs, ... }:
{
  services.mihomo = {
    enable = true;
    tunMode = true;                       # CAP_NET_ADMIN для tun
    webui = pkgs.metacubexd;              # локальный webui (http://127.0.0.1:9090)
    configFile = /var/lib/mihomo/config.yaml;
  };

  # Задизейблить автозапуск службы.
  systemd.services.mihomo.enable = lib.mkForce false;

  # Создать mutable директорию для конфига/подписок.
  systemd.tmpfiles.rules = [
    "d /var/lib/mihomo 0750 mihomo mihomo -"
  ];
}