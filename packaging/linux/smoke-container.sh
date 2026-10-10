#!/usr/bin/env bash
# Run only INSIDE a disposable Ubuntu/Fedora container as root.
# Mount two packages (old, new) built for the container architecture under /packages.
set -euo pipefail
[[ $# == 3 ]] || { echo 'Usage: smoke-container.sh deb|rpm /packages/old /packages/new' >&2; exit 2; }
format=$1; old=$2; new=$3
[[ -f /.dockerenv || -f /run/.containerenv ]] || { echo 'Refusing to install outside a disposable container' >&2; exit 2; }
mkdir -p /tmp/artex-smoke-home/.local/share/artex
export HOME=/tmp/artex-smoke-home
export XDG_DATA_HOME="$HOME/.local/share"
printf 'preserve me\n' > "$XDG_DATA_HOME/artex/user-data-sentinel"
case "$format" in
  deb) dpkg -i "$old"; dpkg -i "$new"; dpkg-query -W artex; dpkg -L artex;;
  rpm) rpm -Uvh "$old"; rpm -Uvh "$new"; rpm -qi artex; rpm -ql artex;;
  *) exit 2;;
esac
[[ $(readlink /usr/bin/artex) == /usr/lib/artex/artex ]]
[[ -x /usr/lib/artex/artex ]]
[[ -f /usr/share/artex/skills/api-recon/SKILL.md ]]
grep -Fx 'Exec=/usr/bin/artex launch' /usr/share/applications/artex.desktop
/usr/bin/artex --help
case "$format" in deb) dpkg --purge artex;; rpm) rpm -e artex;; esac
[[ ! -e /usr/bin/artex && ! -e /usr/lib/artex/artex ]]
grep -Fx 'preserve me' "$XDG_DATA_HOME/artex/user-data-sentinel"
echo 'Package install, upgrade, removal and user-data preservation passed'
