# mw — Master da Web from the terminal

`mw` deploys and runs your apps on Master da Web's serverless platform, and
manages the PostgreSQL and Redis databases and the Object Storage buckets they
use. Everything it does goes through the public API (v1), with an API token
you create in the console, so it works the same from your laptop and from CI.

## Install

```sh
# macOS and Linux
curl -fsSL https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.sh | sh

# Homebrew
brew install masterdaweb/tap/mw

# npm (any system with Node 18+)
npm install -g @masterdaweb/cli
```

In a CI pipeline, the image has `mw` with bash, curl, jq and the PostgreSQL,
MySQL and Redis clients its `connect` commands use:

```sh
docker run --rm -e MW_TOKEN ghcr.io/masterdaweb/mw-cli:<version> deploy --service web
```

On Windows, download `mw_<version>_windows_amd64.zip` from the
[releases](https://github.com/masterdaweb/mw-cli/releases) and put `mw.exe` on
your `PATH`. Every download is checked against the release's `checksums.txt`.

## Sign in

Create a token in the console under **Settings → API tokens**, with the scopes
you need (`serverless:write` to deploy, `serverless:run` for `mw run`,
`databases:write` for databases, `storage:write` for buckets; the `:delete`
scopes only if `mw` should delete things). Then:

```sh
mw login          # paste the token (mwk_…); it is checked and kept for you alone
mw whoami         # the account and the scopes of the token in use
```

In CI, set `MW_TOKEN` instead of running `mw login`.

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
