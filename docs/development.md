# Set up a development machine

This guide covers building, testing, and verifying rsdw-c2 on Windows 11 with Docker Desktop and on Linux.
Production runs in Linux containers, and CI runs on Ubuntu. Windows is a supported place to develop, not to deploy.

## Tools

| Tool | Used for | Version on the reference Windows machine |
| --- | --- | --- |
| Git, with Git Bash | Source, plus the POSIX shell, `sh`, `tar`, `sha256sum`, and GNU `timeout` that scripts and tests use | 2.43 |
| Go | Build and unit tests (`go.mod` requires 1.26 or newer) | 1.27.0 |
| Node.js and npm | Web unit tests, Playwright browser tests, release tooling (`package.json` requires 24.10 or newer) | 24.22.0, npm 11.19 |
| Python | `verification/check-tick-evidence.py` (CI uses 3.13) | 3.13.15 |
| Helm | Chart lint and render checks | 4.3.1 |
| Docker | kind nodes and image builds | Docker Desktop 29.8, WSL2 backend |
| kind and kubectl | Local clusters | kind 0.33.0, kubectl 1.36.1 |
| jq, curl | kind scripts | jq 1.8.2 |
| GitHub CLI | Pull requests | 2.102.0 |
| fnm | Node version management | 1.38.1 |
| prek | Git hook runner. The repository does not currently ship a hook configuration. | 0.5.5 |

## Windows 11

Install Git for Windows first, because Git Bash supplies the shell tools. Then install the rest with winget:

```powershell
winget install Git.Git
winget install GoLang.Go
winget install Schniz.fnm
winget install Python.Python.3.13
winget install Helm.Helm
winget install Kubernetes.kind
winget install Kubernetes.kubectl
winget install jqlang.jq --scope user
winget install GitHub.cli --scope user
winget install Docker.DockerDesktop
winget install j178.Prek
```

Docker Desktop already bundles a `kubectl`; install `Kubernetes.kubectl` only if you want it independent of Docker Desktop.
Open a new terminal after installing so that the PATH change is picked up. Then install Node 24:

```powershell
fnm install 24
fnm default 24
```

### Python

The python.org installer provides `python.exe` but no `python3.exe`, and `scripts/verify.sh` calls `python3`.
Copy it once, and again after you upgrade Python:

```powershell
$py = "$env:LOCALAPPDATA\Programs\Python\Python313"
Copy-Item "$py\python.exe" "$py\python3.exe"
```

Windows also ships Microsoft Store aliases named `python.exe` and `python3.exe` in `%LOCALAPPDATA%\Microsoft\WindowsApps`.
They shadow a real install, and an alias for a removed Store package fails with "cannot find the file".
Turn them off under Settings > Apps > Advanced app settings > App execution aliases.
Turning an alias off deletes its zero-byte stub, so deleting the stubs by hand has the same effect.
Check with `python --version` and `python3 --version` in a new PowerShell and a new Git Bash window; both should print 3.13.

### Shell profiles

Git Bash starts with the PATH it inherited, which can lag behind installs.
The reference machine refreshes PATH from the registry and loads fnm. Put this in `~/.bashrc`:

```sh
# Git for Windows' GNU tools go first so `timeout` and `find` are not the Windows programs of the same name.
if command -v powershell.exe >/dev/null 2>&1; then
  _winpath=$(powershell.exe -NoProfile -Command '[Environment]::GetEnvironmentVariable("Path","Machine") + ";" + [Environment]::GetEnvironmentVariable("Path","User")' 2>/dev/null | tr -d '\r')
  [ -n "$_winpath" ] && export PATH="/mingw64/bin:/usr/bin:$(cygpath -p "$_winpath"):$PATH"
  unset _winpath
fi
command -v fnm >/dev/null 2>&1 && eval "$(fnm env --shell bash)"
```

The order matters. `C:\Windows\system32` contains a `timeout.exe` that waits for a keypress, and `kind-up.sh` needs GNU `timeout`.
Do not paste the output of `fnm env` into `~/.bashrc`; it contains a per-session directory that disappears.

Put this in `$PROFILE.CurrentUserAllHosts` for PowerShell 7:

```powershell
$env:Path = [Environment]::GetEnvironmentVariable("Path","Machine") + ";" + [Environment]::GetEnvironmentVariable("Path","User")
if (Get-Command fnm -ErrorAction SilentlyContinue) { fnm env --use-on-cd --shell powershell | Out-String | Invoke-Expression }
```

### Run everything from Git Bash

Tests and scripts call `sh`, `bash`, `tar`, `find`, `sha256sum`, and `sort`. Run them from Git Bash, or put `C:\Program Files\Git\usr\bin` on PATH.
The repository's `.gitattributes` keeps text files at LF, so scripts check out correctly with `core.autocrlf=true`.

### Keep caches off a full system drive

A Go build, `npm ci`, and a Playwright download need a few gigabytes of scratch space, and Docker Desktop's disk image grows on the system drive.
When the system drive fills, builds fail with "not enough space on the disk" or "cannot allocate memory", and Docker Desktop can hang in "starting".
If `C:` is tight, point the caches at another drive (user environment, new shells only):

```powershell
foreach ($d in 'go-build','gotmp','npm','ms-playwright','tmp') { New-Item -ItemType Directory -Force "D:\dev-cache\$d" | Out-Null }
[Environment]::SetEnvironmentVariable('GOCACHE','D:\dev-cache\go-build','User')
[Environment]::SetEnvironmentVariable('GOTMPDIR','D:\dev-cache\gotmp','User')
[Environment]::SetEnvironmentVariable('npm_config_cache','D:\dev-cache\npm','User')
[Environment]::SetEnvironmentVariable('PLAYWRIGHT_BROWSERS_PATH','D:\dev-cache\ms-playwright','User')
[Environment]::SetEnvironmentVariable('TEMP','D:\dev-cache\tmp','User')
[Environment]::SetEnvironmentVariable('TMP','D:\dev-cache\tmp','User')
```

If Docker Desktop stays in "starting", run `docker desktop restart` after freeing space.

## Linux

Install Go, Node 24.10 or newer, Python 3.13, Helm, kind, kubectl, jq, curl, and Docker from your distribution or the upstream releases.
On Debian or Ubuntu, `apt install curl jq python3 coreutils tar` covers the system tools; get Go, Node, Helm, kind, and kubectl from their own installers, as CI does.
Run Docker without sudo (add yourself to the `docker` group) so the kind scripts work.

## Unit tests

```sh
go build ./...
go vet ./...
go test ./...
```

`go test` takes about one to two minutes on Windows. The tests start `sh` for the restore transaction and a few fixtures, so Git Bash tools must be on PATH.
A few tests assert POSIX file modes. They check the modes on Linux and skip those assertions on Windows, where NTFS does not report them.

## Browser tests

```sh
npm ci --ignore-scripts
npx --no-install playwright install chromium
```

Each file in `tests/` is a standalone Node script. They use the Chromium that Playwright downloads. Set `RSDW_TEST_CHROMIUM` to the path of another Chromium to use that instead.
Most of them start the built server, so build it first. `go env GOEXE` is `.exe` on Windows and empty elsewhere, and the tests look for that name:

```sh
go build -o ".tmp/rsdw-c2$(go env GOEXE)" .
node tests/dashboard-browser.cjs
```

Screenshots and logs land in `.tmp/`, which git ignores.

## Full verification

```sh
bash scripts/verify.sh
```

The script runs the Go tests and vet, the tick evidence check, the web unit tests, the chart checks (`helm lint` and `helm template`), every browser test, and a smoke test of the built binary with `curl`.
It needs `go`, `python3`, `node`, `helm`, `curl`, `mktemp`, and `sed` on PATH, and Playwright's Chromium installed.
It passes unchanged on Windows from Git Bash and on Linux; nothing is skipped.
It does not need Docker or a cluster.

## Local kind cluster

See [Run a local kind cluster](kind.md) for what the scripts do.

```sh
bash scripts/kind-up.sh rsdw-c2-dev
bash scripts/kind-down.sh rsdw-c2-dev
kind delete cluster --name rsdw-c2-dev   # removes the node and its data
```

`kind-up.sh` needs `docker`, `kind`, `kubectl`, `timeout`, `curl`, and `jq`.

Findings from Windows 11 with Docker Desktop 29.8 (WSL2 backend):

- Creating a cluster pulled `kindest/node:v1.37.0` and took about 2.5 minutes; the first run needs the image download. Resuming a stopped node took about 40 to 65 seconds, including the Metrics Server rollout.
- The scripts pass `--raw=/readyz` and similar arguments to `kubectl`. Git Bash rewrites such arguments into Windows paths (`C:/Program Files/Git/readyz`), which made the API readiness check fail forever.
  The kind scripts now set `MSYS2_ARG_CONV_EXCL` to leave `--raw=`, `RSDW_CHART=`, and `KUBECONFIG=` values alone. This has no effect on Linux.
- GNU `timeout` is `/usr/bin/timeout` in Git Bash, provided the `~/.bashrc` above keeps `/usr/bin` ahead of `C:\Windows\system32`.
- Docker Desktop must have more than the 3 GiB the script gives the node; the reference machine's VM has 15.6 GiB.
- `kind-up.sh`, `kind-down.sh`, and `kind delete cluster` were run in that order and left no cluster or node container behind.
- `scripts/kind-smoke.sh` builds images and creates its own cluster. It got the same path-conversion guard but was not run on Windows.

## What stays Linux-only

- Production images and the Helm chart target Linux containers.
- The restore transaction script runs in the world pod. In demo mode it runs on the host with `sh`, using paths converted to `/c/...` form on Windows.
- Directory `fsync` is a no-op on Windows, where directories cannot be opened for writing; NTFS journals directory metadata instead. Linux behavior is unchanged.
- The backup repository writer lock uses `flock` on Unix and `LockFileEx` on Windows.
