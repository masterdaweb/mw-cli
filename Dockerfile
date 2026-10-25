# The `mw` image GoReleaser publishes with each release
# (ghcr.io/masterdaweb/mw-cli:<version>, .goreleaser.yaml "dockers"): for CI
# pipelines, and for the platform's hourly synthetic journey
# (mw-serverless-platform, infrastructure/synthetic). Beside `mw`, what its
# commands and a script need: bash, curl and jq, and the PostgreSQL, MySQL/
# MariaDB and Redis clients `mw db connect` and `mw redis connect` start.
# GoReleaser puts the binary it built in the build context; nothing is
# compiled here.
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache bash ca-certificates curl jq mariadb-client postgresql18-client valkey-cli
COPY mw /usr/local/bin/mw
USER 65534:65534
ENTRYPOINT ["mw"]
