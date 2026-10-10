Native Linux packages are built by `scripts/package-linux.sh --binary PATH --version 0.3.18 --arch amd64|arm64 --format deb|rpm --outdir PATH`. Supply the binary compiled with its corresponding managed build channel (`deb` or `rpm`). The script checks the ELF machine before packaging; it does not execute the target binary.

The standalone packager is pinned to nFPM 2.47.0. Official primary sources: [installation](https://nfpm.goreleaser.com/docs/install/), [configuration](https://nfpm.goreleaser.com/docs/configuration/), and [release checksums](https://github.com/goreleaser/nfpm/releases/download/v2.47.0/checksums.txt). Bootstrap verifies pinned SHA256 hashes and extracts only into a temporary directory. `NFPM_BIN` can select an already provisioned 2.47.0 executable. No GoReleaser Pro license is needed.

Packages install static files below `/usr`; there are no install/remove hooks, service units, database provisioning, or home-directory writes. The desktop entry invokes `artex launch`. Runtime configuration and private data belong to `$XDG_DATA_HOME/artex` (default `~/.local/share/artex`), handled by the launcher. External reconnaissance tools such as nmap are optional doctor-reported prerequisites rather than bundled dependencies.

Inspect each artifact without installing it:

```sh
python3 packaging/linux/verify-package.py dist/artex-0.3.18-linux-amd64.deb amd64
python3 packaging/linux/verify-package.py dist/artex-0.3.18-linux-arm64.rpm arm64
```

For acceptance, use disposable containers matching the package architecture, with two different release versions mounted under `/packages`. Never run these commands on the user's host:

```sh
docker run --rm --platform linux/amd64 -v "$PWD/dist:/packages:ro" -v "$PWD/packaging/linux:/checks:ro" ubuntu:24.04 bash /checks/smoke-container.sh deb /packages/artex-0.3.17-linux-amd64.deb /packages/artex-0.3.18-linux-amd64.deb
docker run --rm --platform linux/amd64 -v "$PWD/dist:/packages:ro" -v "$PWD/packaging/linux:/checks:ro" fedora:43 bash /checks/smoke-container.sh rpm /packages/artex-0.3.17-linux-amd64.rpm /packages/artex-0.3.18-linux-amd64.rpm
```

Repeat with `--platform linux/arm64` and arm64 filenames. The smoke check verifies native package queries, upgrade, CLI execution, launcher metadata, payload, symlink, removal, and preservation of a user-data sentinel. A graphical browser launch and first-run database connection require separate desktop acceptance; a headless package smoke does not establish those outcomes.
