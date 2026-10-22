# Clash Verge (GUI поверх mihomo) — фоллбек с графическим интерфейсом.
#
# Нет модуля services.clash-verge: Clash Verge — это GUI-приложение, а не
# системная служба. В NixOS он подключается через programs.clash-verge, а
# service mode (юнит clash-verge) включается флагом serviceMode. Чтобы служба
# не стартовала при загрузке, переопределяем systemd.services.clash-verge.enable.
{ config, lib, pkgs, ... }:
{
  programs.clash-verge = {
    enable = true;
    serviceMode = true;   # IPC-служба для tun (нужна GUI)
    tunMode = true;       # setcap CAP_NET_ADMIN
    autoStart = false;    # GUI не автозапускать при логине
  };

  # Служба задизейблена: юнит есть, запускается вручную.
  systemd.services.clash-verge.enable = lib.mkForce false;
}