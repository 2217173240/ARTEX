# Narrow go-mitmproxy runtime replacement

Upstream: https://github.com/lqqyt2423/go-mitmproxy, module version **v1.9.2**.
Source module checksum: `h1:+QTbDyUUaVRmEavPq0/eWzJ2K+K4JdL4ff91s/adnxk=`.
Downloaded module ZIP SHA-256: `0abd197455a47fc6045176cb29727959e51686de62d8bcd09be000236c3744d2`.
`UPSTREAM-MANIFEST.json` records SHA-256 of every retained upstream file before patching.
All retained upstream files preserve their upstream contents except the three differences below;
upstream LICENSE remains applicable. Tests and this provenance metadata are ARTEX additions.

Only semantic production change: remove `conn.Close()` after SOCKS5 `DialContext` returns
an error in `internal/helper/proxy.go`. The actual x/net SOCKS dialer returns nil
on failure and already closes an established connection after handshake failure.
The error is returned unchanged. There is no general panic recovery, direct-route
fallback, authentication change, or dialer replacement.

Two additional whitespace-only changes keep Git whitespace checks clean:
remove the extra EOF blank line in `proxy/flow.go` and remove trailing tabs on
the blank line after SetFormatter in `proxy/instance_logger.go`. Neither changes
Go behavior. `UPSTREAM-MANIFEST.json` deliberately retains original upstream
hashes; these two files and `internal/helper/proxy.go` are the exact three
differences against that manifest.

Fix idea: 6ccg, https://github.com/6ccg/ARTEX/commit/cb5dfec8fcc3844bd7d56f3e208ff1cb1a34d4b0
(author `6ccg <44950572+6ccg@users.noreply.github.com>`). This applies the same
one-line deletion to official v1.9.2, rather than adopting that fork's vendored tree.

Retained runtime packages: `cert`, `internal/helper`, `proxy`; upstream module
metadata and LICENSE. No addon, command, example, frontend, or upstream tests are
included. This replacement supports ARTEX's runtime imports, not the full upstream
module's public package surface. Future imports may require explicitly adding their
runtime sources and manifest entries. Upgrades require refreshing retained files,
checking the minimal patch and manifest, and rerunning helper and traffic tests.
Remove the replacement once an upstream release fixes the actual helper.
