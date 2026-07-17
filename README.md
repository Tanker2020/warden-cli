# warden — the Nyxtra CLI

`warden` is the terminal client for the Nyxtra platform: authenticate once in
the browser, then validate and deploy Security-as-Code rules to your hosted
SaC + Warden environment, fire test events at it, and connect your AWS
account — all without touching servers.

```text
warden login          sign in via the Nyxtra web portal (device flow)
warden whoami         who the stored token belongs to
warden status         your tenant's provisioning/subscription state
warden validate       parse + lint a .sac file locally (embedded compiler front-end)
warden deploy         push rules to your hosted environment (--list / --remove too)
warden test           send a synthetic event through the live pipeline
warden aws configure  connect your AWS account (keys never leave your machine)
warden logout         remove stored credentials
```

## Install

macOS / Linux:

```sh
./install.sh
```

Windows (PowerShell):

```powershell
powershell -ExecutionPolicy Bypass -File install.ps1
```

Both scripts handle everything:

- find your Go toolchain, or download one **privately** to
  `~/.warden/toolchain` if none exists (no sudo/admin, nothing system-wide),
- download module dependencies,
- build and install `warden` — to `~/.local/bin` on macOS/Linux
  (override with `WARDEN_INSTALL_DIR=/somewhere ./install.sh`), or
  `%LOCALAPPDATA%\Programs\warden` on Windows (`$env:WARDEN_INSTALL_DIR`
  overrides),
- put the install dir on your PATH — one line in your shell rc on
  macOS/Linux, the persistent *user* PATH on Windows — only if it isn't
  already there. Open a new terminal afterwards.

The CLI embeds its language front-end (`.sac` parser + classifier) from the
public **`github.com/Tanker2020/sac-lang`** module. Until that module is pushed
and tagged, `go.mod` carries a local `replace` pointing at a sibling checkout
(`../sac-lang`), and the install script checks for it. Once `sac-lang` is
published and the `replace` line is removed, a clean clone builds with **no
sibling repos** — nothing private required.

## First run

```sh
warden login       # prints a code, opens the browser: sign in → type the code → Authorize
warden deploy --file rule.sac
warden deploy --list
warden test --alarm <alarm-name-your-rule-matches>
```

`login` needs no configuration: the Nyxtra address (`https://nyxtra.dev`) is
baked into the binary, like `gh` knows github.com.

**Dev setup** (while nyxtra.dev isn't live): mark the machine as dev and
`warden login` targets the local dev server (`http://localhost:5173`) instead —

```sh
mkdir -p ~/.warden && echo 'WARDEN_ENV=dev' > ~/.warden/env   # machine-wide
# or: cp .env.example .env      (repo-local, applies when run from this dir)
# or one-off: warden login --dev
```

Precedence: `--issuer`/`--dev` flag → `WARDEN_ISSUER` env → where you last
signed in (remembered even after `warden logout`) → `WARDEN_ENV=dev` dev
default → production. Login always prints which deployment it's talking to.

Credentials are stored in `~/.warden/credentials.json` (override the
directory with `WARDEN_HOME`). Tokens are Better Auth session tokens — when
one expires, `warden login` again.

| Env var | Meaning |
| --- | --- |
| `WARDEN_ISSUER` | override the Nyxtra base URL for `login` (defaults: last login's, else the baked-in dev URL) |
| `WARDEN_CONTROL_PLANE` | control-plane URL if different from the issuer (rare) |
| `WARDEN_TOKEN` | bearer token override for scripting/CI |
| `WARDEN_HOME` | credentials directory (default `~/.warden`) |
| `WARDEN_NO_BROWSER` | set to stop `login` opening a browser |
| `WARDEN_INSTALL_DIR`, `WARDEN_GO_VERSION` | install.sh knobs |

## How deploys work

`warden deploy` validates every file locally first (same parser the server
runs), then sends the bundle to the Nyxtra control plane, which looks up your
org's container and forwards the rules to it with a platform-held token — the
CLI never talks to tenant infrastructure directly, and single-file deploys
are merged additively against what's live.

## Development

```sh
go build ./...    # needs ../sac-lang checked out (local replace directive)
go test ./...
go run ./cmd/warden --help
```

The `.sac` front-end lives in `github.com/Tanker2020/sac-lang`. To publish it:
push the `sac-lang/` module to that repo, tag `v0.1.0`, then delete the
`replace github.com/Tanker2020/sac-lang => ../sac-lang` line in `go.mod` — after
that a clean clone of this repo builds on its own.
