# mw — Master da Web from the terminal

`mw` runs your Master da Web cloud account from a terminal, a script or CI:
VPS instances, serverless apps, managed databases, Object Storage, DNS,
firewalls, VPCs, VPN gateways and the Security Center — what the console does,
through the public API.

## Install

```sh
npm install -g @masterdaweb/cli
mw --version
```

Or once, without installing: `npx @masterdaweb/cli instances`.

The package fetches the `mw` binary for your system (macOS, Linux or Windows;
x64 or arm64) from the GitHub release of the same version and checks it
against that release's `checksums.txt`.

Other ways to install: Homebrew (`brew install masterdaweb/tap/mw`), a script
(`curl -fsSL https://raw.githubusercontent.com/masterdaweb/mw-cli/main/install.sh | sh`),
Docker (`docker run --rm -it -e MW_TOKEN ghcr.io/masterdaweb/mw-cli instances`)
or the binaries on the [releases page](https://github.com/masterdaweb/mw-cli/releases).

## Sign in

```sh
mw login     # shows a code and opens the console to approve this computer
mw whoami
```

In CI, set `MW_TOKEN` to an API token created in the console under
Settings → API tokens.

## Examples

```sh
mw instances
mw instances create --name web-1 --plan c2m4 --image ubuntu-24-04 --ssh-key laptop --wait
mw dns records set example.com www A 203.0.113.10
mw deploy                       # the app in this folder, serverless
mw db create shop-db --engine postgres --location dallas   # where it runs: `mw regions`
mw security                     # your security score and what lowers it
mw vpn users create office-vpn maria --email maria@example.com --apply
```

Every list prints a table, or JSON or YAML with `-o json` / `-o yaml`.
`mw help` and `mw <command> --help` describe everything.

## Links

- Source and full documentation: https://github.com/masterdaweb/mw-cli
- Terraform provider: https://registry.terraform.io/providers/masterdaweb/masterdaweb
