# Installation

Orbit ships as a single native binary, `docker-orbit`, that works both as a standalone CLI (`docker-orbit …`) and as a Docker CLI plugin (`docker orbit …`). **No Go toolchain, source checkout, or container wrapper is required.**

> The previous container-wrapper installer was retired. Orbit's CLI is a *host-side* tool — it writes rollback state to `/tmp/orbit-<service>-state.json` and history to `$XDG_STATE_HOME/orbit` — so running it inside an ephemeral container silently loses that state. Native binary only.

## Supported platforms

| OS | Architectures |
|----|----|
| Linux | amd64, arm64 |
| macOS | amd64, arm64 |

(The *proxy* runs in a container — that is separate from CLI installation and is pulled automatically when you deploy.)

---

## Production installation

### Option A — install script (recommended)

```bash
curl -fsSL https://raw.githubusercontent.com/docker-secret-operator/orbit/main/install.sh | bash
```

The script detects your OS/arch, downloads the matching release archive, **verifies its SHA256 checksum**, and installs `docker-orbit` into your Docker CLI plugins directory. Pin a version with `ORBIT_VERSION=vX.Y.Z`.

### Option B — Linux packages (deb / rpm)

```bash
# Debian/Ubuntu
sudo dpkg -i docker-orbit_<version>_linux_amd64.deb
# RHEL/Fedora
sudo rpm -i docker-orbit_<version>_linux_amd64.rpm
```

Packages install the binary to `/usr/bin/docker-orbit` and symlink it into `/usr/local/lib/docker/cli-plugins/` so both `docker-orbit` and `docker orbit` work.

### Option C — download a binary manually

1. Download `docker-orbit_<version>_<os>_<arch>.tar.gz` and `checksums.txt` from the [Releases](https://github.com/docker-secret-operator/orbit/releases) page.
2. Verify (see [Checksum verification](#checksum-verification)).
3. Extract and copy into the plugins directory:
   ```bash
   tar -xzf docker-orbit_*_linux_amd64.tar.gz
   sudo install -m 0755 docker-orbit /usr/local/lib/docker/cli-plugins/docker-orbit
   ```

After any method: `docker orbit version` and `docker orbit doctor`.

---

## Development installation

For contributors working from source:

```bash
# Build + install the local source as a plugin (needs Go)
make install-plugin

# Or build a full release set locally (archives, checksums, deb, rpm) into ./dist
make dist

# Build a snapshot and install it via the native installer (no download)
make install-local
```

`go install` also works once the repo is public:

```bash
go install github.com/docker-secret-operator/orbit/cmd/docker-orbit@latest
```

### Local snapshot testing (no publishing required)

The installer accepts a local directory of GoReleaser artifacts, so the entire install flow can be tested before anything is published:

```bash
make dist                                   # produces ./dist
ORBIT_DIST_DIR=./dist PLUGIN_DIR=/tmp/plugins ./install.sh
```

---

## Upgrade process

Re-run the same installation method. The installer replaces **only** the binary, in place, at whichever plugins directory it is already installed in. Orbit keeps no package-managed configuration and never touches runtime state (`/tmp/orbit-*`, `$XDG_STATE_HOME/orbit`) on upgrade, so upgrades are safe and non-destructive.

- Script/binary: re-run `install.sh` (or `ORBIT_VERSION=vX.Y.Z curl … | bash` to pin).
- deb/rpm: `sudo dpkg -i …` / `sudo rpm -U …`.

## Uninstall process

Non-destructive — removes the plugin only, never your deployment state:

```bash
# script / manual install
sudo rm /usr/local/lib/docker/cli-plugins/docker-orbit   # or ~/.docker/cli-plugins/docker-orbit

# packages
sudo apt remove docker-orbit    # or: sudo rpm -e docker-orbit
```

---

## Checksum verification

Every release ships a `checksums.txt` (SHA256). The install script verifies automatically and **refuses to install a binary whose checksum does not match**. To verify manually:

```bash
# Linux
sha256sum -c checksums.txt --ignore-missing
# macOS
shasum -a 256 -c checksums.txt --ignore-missing
```

Builds are reproducible (pinned commit timestamp, `CGO_ENABLED=0`), so the same source + tag always yields the same binary and checksum. The pipeline is structured to add code signing (cosign/GPG) later without redesign.

---

## Verify plugin discovery

```bash
docker orbit version     # prints the installed version
docker --help | grep orbit   # 'orbit*' appears under plugin commands
docker orbit doctor      # environment audit
```

If `docker orbit` isn't found but `docker-orbit` works, the binary landed outside a directory Docker scans — move it to `/usr/local/lib/docker/cli-plugins/` (all users) or `~/.docker/cli-plugins/` (current user).
