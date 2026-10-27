# mw — Master da Web from the terminal

`mw` is everything the Master da Web console does, from a terminal or a CI job:
VPS instances (create, power, rescue, reinstall, resize, clone, snapshots,
backups, your own images), firewalls, VPCs, DNS and reverse DNS, serverless
apps, managed databases, Object Storage, projects, billing and the activity
log. Everything goes through the public API (v1), so it works
the same from your laptop, a server over SSH, and CI.

## Install

```sh
# macOS and Linux
curl -fsSL https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.sh | sh

# Homebrew
brew install masterdaweb/tap/mw

# npm (any system with Node 18+)
npm install -g @masterdaweb/cli
```

```powershell
# Windows (PowerShell)
irm https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.ps1 | iex
```

Every download is checked against the release's `checksums.txt`.
`MW_VERSION=1.2.3` picks a version, `MW_INSTALL_DIR` a folder.

In a CI pipeline, the image has `mw` with bash, curl, jq, an SSH client, git
and the PostgreSQL, MySQL and Redis clients its `connect` commands use:

```sh
docker run --rm -e MW_TOKEN ghcr.io/masterdaweb/mw-cli:<version> instances -o json
```

The same image is the console's **Cloud Shell**: a terminal in the browser
where `mw` is already signed in to your account.

Shell completion: `mw completion bash|zsh|fish|powershell` (Homebrew installs
it for you).

## Sign in

```sh
mw login          # shows a code and opens the console: approve it there
mw whoami         # the account and the scopes of the token in use
mw logout         # revokes this computer's token and forgets it
```

`mw login --scopes vm:read,dns:read` asks for fewer permissions: a token that
can only read, say. Without it the token gets every scope your account can
grant.

`mw login` works over SSH too: open the link it prints on any device and
confirm the code. The CLI gets a token of its own, named after the computer
and revocable under **Settings → API tokens**.

In CI, set `MW_TOKEN` (a personal access token, `mwk_…`, with the scopes the
job needs) instead of running `mw login`; or save one with
`mw login --with-token < token.txt`.

## Output

Every list prints a table; `-o json` or `-o yaml` print what the API answered
(for `jq` and scripts; `--json` is the same as `-o json`), `-q` prints the ids
alone and `--no-headers` drops the header line:

```sh
mw instances -q | xargs -n1 mw instances reboot --wait
mw instances get web-1 -o json | jq -r '.ip_addresses[0].address'
```

Commands that change something ask first (`--yes` answers for scripts);
irreversible ones — destroying or reinstalling an instance, restoring a
snapshot or backup, a resize that releases an address, removing a DNS zone —
ask you to type the id or name back. Commands
whose work goes on after they answer take `--wait` (and `--timeout`). A failed
command exits non-zero and says why, with the request id to quote to support.

## VPS instances

```sh
mw regions && mw plans && mw images            # where, what size, what system or app
mw ssh-keys add                                # ~/.ssh/id_ed25519.pub (or a file)
mw instances create --plan c2m4 --image ubuntu-24-04 --ssh-key work --name web-1 --wait
mw instances create --plan c4m8 --image wordpress --subscription <id> --project loja --yes
mw instances create --plan c2m4 --image debian-12 --user-data @cloud-init.yaml
mw instances                                   # list; `mw instances get web-1`: image, project, subscription
mw instances reboot web-1 --wait               # start, stop, reboot; `status` for live state
mw instances reinstall web-1 --image debian-12 --ssh-key work
mw instances reset-password web-1              # through the guest agent, no reboot
mw instances metrics web-1 --timeframe day     # CPU, memory, disk and network
mw instances bandwidth web-1                   # traffic this cycle, against the plan
mw instances rescue enter web-1 --ssh-key work # boot a rescue system to repair it
mw instances resize-options web-1              # the plans it can move to, with prices
mw instances resize web-1 --plan c4m8 --wait   # priced first; a CPU/memory change restarts it
mw instances resize web-1 --disk-extra-gb 50 --addon backup-slot=2
mw instances clone web-1 staging --wait        # staging's disks become web-1's; type its id
mw instances delete web-1                      # type its id to confirm
```

`create` prices the configuration first (`mw orders quote` does only that)
and orders it once you confirm. A new subscription raises an invoice and the
instance is built once it is paid (`--wait` follows the payment and the
build); added to an existing subscription it is paid from its balance.

`resize` moves an instance to another plan by its code, the way sizes and
plans work elsewhere. It shows the new rate before anything changes, and on a
commitment what is charged now for the rest of the cycle, or what a downgrade
gives back as credit. When the balance is short it says how much to add with
`mw subscriptions top-up`. A grown disk needs its partition grown inside the
guest afterwards. `delete` says first what it does to the bill, and when the
API would refuse it (a move, transfer or conversion pending).

```sh
mw snapshots create web-1 --name before-upgrade   # kept 30 days
mw snapshots restore web-1 before-upgrade
mw backups run web-1 --wait                       # with the backup add-on and a free slot
mw backups restore web-1 latest                   # or `mw backups restore <backup-id>` alone
mw backups schedule set web-1 --days mon,thu --time 03:30 --timezone America/Sao_Paulo
```

Your own images: a disk image (qcow2, raw, img, vmdk) builds instances, and
an ISO boots an installer. An upload goes straight to storage in parts, four
at a time, picks up where it stopped when the connection drops (`--resume`
after an interrupted run), and is checked and converted before use:

```sh
mw custom-images upload debian-12.qcow2 --name debian-12 --wait
mw custom-images update debian-12 --cloud-init --default-username debian
mw custom-images                                  # yours, with their state; `get`, `quota`, `delete`
mw instances create --plan c2m4 --custom-image debian-12
mw instances iso attach web-1 alpine-3.20         # an ISO in the drive; reboot to boot from it
mw instances iso detach web-1
```

## Network

```sh
mw firewalls create web
mw firewalls rules add web --direction in --action ACCEPT --macro SSH --source 203.0.113.0/24
mw firewalls rules add web --direction in --action ACCEPT --protocol tcp --port 80,443
mw firewalls update web --policy-in DROP          # what no rule allows is dropped
mw firewalls attach web web-1 --wait              # protect web-1 (and apply the rules)

mw vpcs create backend --subnet 10.20.0.0/24      # a private network between instances
mw vpcs attach backend web-1 --restart --wait     # same region; the NIC comes up on restart
mw vpcs attach backend web-2 --private-ip 10.20.0.10
mw vpcs get backend                               # its instances and their private addresses

mw dns zones create loja.com.br
mw dns zones health loja.com.br                   # is the registrar pointing here yet?
mw dns records set loja.com.br www A 203.0.113.10
mw dns records add loja.com.br @ MX "10 mail.loja.com.br."
mw rdns set 203.0.113.10 mail.loja.com.br         # reverse DNS, forward-confirmed
```

A VPN gateway is an instance ordered with the VPN gateway image. People connect
with the OpenVPN app, each with their own login; changes are saved, then put in
place by `mw vpn apply` (or `--apply` on the change):

```sh
mw vpn                                            # the gateways, and whether changes wait
mw vpn users create office maria --email maria@loja.com.br --send-credentials --apply
mw vpn users update office maria --status inactive --apply
mw vpn profile office office.ovpn                 # the file people import (0600)
mw vpn update office --pool 10.9.0.0/24
mw vpn apply office --wait                        # asks for --rebuild when it takes one
mw vpn sites office                               # site-to-site tunnels and their state
```

## Projects

A project groups what belongs to one system or client — instances, serverless
services, databases and buckets:

```sh
mw projects create loja
mw projects resources add loja instance web-1 web-2
mw projects resources add loja bucket loja-fotos
mw projects resources loja                         # what it holds
mw instances create … --project loja               # made straight into it
```

## Billing

```sh
mw subscriptions                                   # what instances are billed under
mw subscriptions balance <id>
mw subscriptions top-up <id> --amount 200          # raises an invoice (Pix, boleto, card)
mw invoices --open
mw operations                                      # background work and how it went
```

## Activity

Who did what in the account — a person in the console, an API token, an AI
assistant, support or the platform — from where, and how it went:

```sh
mw activity                                        # newest first
mw activity --category instance --since 24h
mw activity --outcome denied --actor api-token --since 7d
mw activity --target-type instance --target-id 24377
mw activity --follow                               # keeps printing what happens
```

## Security Center

The account's security score and what lowers it, riskiest first — instances
are scanned from outside, the rest is checked from how it is set up:

```sh
mw security                                        # the score, open findings, the worst resources
mw security findings --severity high
mw security finding <id>                           # why it matters and what to do
mw instances scan web-1 --wait                     # check again after a fix
mw security accept-risk --port 3306 --instance db-1 --justification "…" --until 90d
mw security exceptions                             # the risks accepted; `mw security revoke <id>`
mw security settings set --frequency monthly
```

## Deploy a folder

```sh
cd my-app
mw init                    # a project and a service for this folder, and a link to it
mw deploy                  # packs the folder, sends it, follows the build until it is live
```

`mw init` names both after the folder (`--project`, `--name`, `--kind`,
`--port` choose otherwise). The folder is linked in `.mw/link.json`, so every
command run inside it acts on that service without `--service`.

What is sent: the folder minus what its `.mwignore` says, or else what its
`.gitignore` files say (each for its own directory), in git's syntax. `.git`
and `.mw` are never sent. The archive must fit the size the platform takes;
leave dependencies and build output out.

Services built from Git deploy their branch instead (`mw deploy`, or
`mw deploy --ref v1.4.2`); services that run an image deploy a new one
(`mw deploy --image ghcr.io/acme/api:1.5`).

## Publish a repository

```sh
mw launch ana/loja                           # a repository connected in the console (deploys on every push)
mw launch https://github.com/ana/loja        # or a public one on GitHub, GitLab or Bitbucket
mw launch ana/loja --without scheduler --yes # leave part of the proposal out, without being asked
mw launch ana/loja --dry-run                 # only see the proposal
```

`mw launch` reads a few files of the repository and proposes the project it
needs: the site and the command that migrates before each release, the queue
worker and scheduler it has, the databases, Redis and bucket it uses, the
secrets it generates, what is left to fill in and what it costs a month. Say
yes and it is all made, bound and followed until it is live. The project is
named after the repository (`--name` chooses otherwise); `--branch` publishes
another branch than the default one. Connect GitHub, GitLab or Bitbucket in
the console (**Settings › Integrations**); with several accounts connected,
`--provider` or `--connection` picks one.

## Start from a template

```sh
mw templates                                 # the gallery: Laravel, Next.js, Django, FastAPI, Go, PHP + MySQL, n8n…
mw templates show laravel                    # what it makes, what it costs a month, what it asks
mw templates deploy laravel --name loja      # the whole project: services, databases, bucket, wired
```

A template makes the project in one go — its services built from the
template's starter, its databases and bucket, the variables that join them
(passwords generated, never typed) — and follows each service until it is
live. Templates that ask something take `--answer KEY=VALUE`
(`mw templates show` lists the keys). To change the code, copy the starter to
your GitHub and connect the copy: **Your code** on the project's page in the
console.

## Every day

```sh
mw status                         # state, address, instances, last deployment
mw logs -f                        # the logs, as they come
mw logs --access --status 5xx     # the requests it answered: method, status, path, time taken
mw metrics --range 24h            # requests, errors, response times, instances, CPU, memory
mw deployments                    # deployments; --revisions for what a rollback can go back to
mw deployments cancel             # stop the deployment under way (before its release)
mw rollback 3                     # back to revision 3, at once, without a build
mw redeploy --no-cache            # build the same code again
mw restart                        # new instances, nothing rebuilt
mw scale --min 1 --max 5          # always one awake, up to five under load
mw scale --size medium            # a bigger instance, with a new revision

mw env                            # the variables (a secret is never shown)
mw env set APP_ENV=production
mw env set --secret STRIPE_KEY=sk_live_…
mw env import .env --secret DB_PASSWORD
mw env pull > .env.local          # every variable but the secrets

mw domains add app.loja.com.br    # prints the DNS records to create
mw domains check app.loja.com.br
```

`mw metrics` also says, once a week of use shows it, the size the service
should have (`mw scale --size` applies it). Access logs never hold the query
string; `--search` looks in the path.

## Alerts and webhooks

```sh
mw alerts                                         # a service's alerts, and whether one fires now
mw alerts set latency --threshold 800ms --window 15
mw alerts set memory --threshold 85%
mw alerts set error-rate --off                    # or --on; --email=false: console and webhooks only
mw webhooks add https://loja.com.br/hooks/mw --events deployment.failed,alert.*
mw webhooks add https://hooks.slack.com/services/T000/B000/XXXX   # Slack and Discord are recognised
mw webhooks                                       # every webhook, its state and last delivery
mw webhooks get 0193f0e2                          # one: its events, state and failures
mw webhooks test 0193f0e2                         # a webhook.test event now, and the answer
mw webhooks deliveries 0193f0e2                   # its last 50 deliveries
mw webhooks rotate 0193f0e2                       # a new signing secret, shown once
mw webhooks remove 0193f0e2
mw webhooks events                                # what a webhook can get
```

Every web and private service starts with four alerts on: requests failing
(`error-rate`), slow responses (`latency`), memory near its limit (`memory`)
and the maximum instances reached (`max-instances`); workers and cron jobs
watch memory. A webhook to your own address gets each event as JSON with
`MW-Signature: t=…,v1=…` — the HMAC-SHA256 of `<t>.<body>` with the secret
shown once when it is made. One that keeps failing is switched off, and you
are told. Removing and rotating ask first (`--yes` answers for a script).

## Projects and services

```sh
mw projects                                   # every project, its services and their state
mw services --project loja                    # a project's services, with their addresses
mw services create api --repo ana/loja --branch main --project loja   # built from a repository, deployed on each push
mw services create api --image ghcr.io/acme/api:1.4 --project loja --port 8080
mw projects duplicate loja --name loja-staging    # a staging: same services, new empty databases and buckets
mw projects update loja --freeze "Black Friday"   # no deployment until --unfreeze
mw projects pause loja-staging                    # every service stops ("temporarily unavailable") until resume
mw services hook web                              # a URL any CI can POST to deploy (shown once)
mw services update web --deletion-protection=true
mw projects update loja-staging --deletion-protection=false
mw projects delete loja-staging --confirm loja-staging
```

Deleting takes deletion protection off first and the name typed back. A
token needs `serverless:delete` for it; turning protection off or on is a
`serverless:write` change.

## Shared variables and private images

```sh
mw groups --project loja                                     # a project's variable groups
mw groups create shared --project loja
mw groups set shared MAIL_HOST=smtp.loja.com.br --project loja   # redeploys the services reading it
mw groups use shared --service worker --project loja         # which groups a service reads, in order
echo "$GHCR_TOKEN" | mw registry add ghcr.io --username ana --password-stdin
mw services create api --project loja --image ghcr.io/ana/api:1.4 --registry ghcr.io
```

A service's own variables win over its groups', and later groups over earlier
ones. A registry password or token is read from standard input only, never
from an argument, and is never shown again.

## Commands in a service

```sh
mw run                                   # a shell in the live revision
mw run -- php artisan tinker             # a command, this terminal attached to it
mw run -- php artisan migrate --force    # exits with the command's code (CI can rely on it)
mw run --no-tty -- 'psql "$DATABASE_URL"' < consulta.sql   # input piped in, then its end
```

`mw run` starts a new container of the service's live revision — its image, its
variables (secrets included), its size. It takes no traffic, is billed by the
second while it runs and ends with the command, after an hour at the latest. The
token needs `serverless:run`: a token that deploys cannot open a shell. Every run
is in the service's activity log and on its Terminal tab in the console.

## Databases

```sh
mw db sizes                                  # sizes on offer, with prices
mw db create loja-prod --extension vector    # PostgreSQL; ready in a few minutes
mw redis create filas                        # Redis
mw db connect loja-prod                      # psql, through the platform, the password passed for you
mw db tunnel loja-prod --port 15432          # a port on this computer for any tool (DBeaver, an ORM)
mw db url loja-prod --direct                 # the connection URL, for your own tools
mw db bind loja-prod --service web --layout framework   # DB_* variables, a login of its own
mw db resize loja-prod --size pg-2-4         # a short restart; billed at the new size from now on
mw db allow loja-prod 203.0.113.7            # let your office connect too
```

`mw db connect` needs the database's client installed (`psql`, `mysql`,
`mariadb` or `redis-cli`). It connects through the platform, so the database
need not allow your address; TLS stays between the client and the database,
verified against the database's own name (`--no-tunnel` connects to its address
instead, once `mw db allow` lets yours). The password reaches the client through
its environment or a file only you can read, never its command line. `mw db
export` and `mw db import` connect the same way.

## Object Storage

```sh
mw storage create loja-uploads
mw storage keys create backup-job --bucket loja-uploads --read-only
mw storage bind loja-uploads --service web --layout framework   # AWS_* variables, as Laravel reads them
mw storage info                    # the S3 endpoint and region, for aws, rclone and the SDKs
mw storage ls loja-uploads/produtos/
mw storage presign loja-uploads relatorio.pdf --expires 24h      # a link that downloads it, up to 7 days
mw storage presign loja-uploads novo.csv --upload --content-type text/csv
mw storage rm loja-uploads produtos/antigo.jpg
```

## Money

```sh
mw wallet               # the credit, and how long it lasts at the current pace
mw wallet topup 100     # an invoice to pay by Pix, boleto or card
mw wallet cap set 500 --stop   # the most a period may cost; --stop stops the services at it
mw wallet cap remove
mw wallet recharge on 100      # a top-up invoice on its own at 3 days of credit left
mw wallet recharge off
mw usage                # this period so far, per service and database
mw egress               # what services sent to visitors, of what the period includes
```

## Scripts

Every command takes `--json` and prints the API's answer as JSON. Errors go
to standard error with what to do next, and the exit code is not zero. Global
flags: `--token` (this command only), `--api-url`, `--json`.

## Build

```sh
go build -o mw ./cmd/mw
go test ./...
```

Releases: `git tag vX.Y.Z && goreleaser release --clean` builds every
system's binary, the archives and their checksums, and updates the Homebrew
formula. The npm package (`npm/`) fetches the archive of its own version from
the release.
