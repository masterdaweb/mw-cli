# The `mw` image GoReleaser publishes with each release
# (ghcr.io/masterdaweb/mw-cli:<version>, .goreleaser.yaml "dockers"): for CI
# pipelines, for the platform's hourly synthetic journey
# (mw-serverless-platform, infrastructure/synthetic), and for the console's
# Cloud Shell — a terminal in the browser where `mw` is already signed in.
#
# Beside `mw`, what its commands, a script and a person at a shell need: bash
# (with completion), curl, jq, an SSH client and git, an editor and a pager,
# and the PostgreSQL, MySQL/MariaDB and Redis clients `mw db connect` and
# `mw redis connect` start. GoReleaser puts the binary it built in the build
# context; nothing is compiled here. No credential is ever baked in: the
# token comes from the environment (MW_TOKEN, MW_API_URL) at run time.
FROM alpine:3.24.3@sha256:be2656061b536c1883c4034790e21d0a09c5aaaf51968b89368a3c0371ea2c9a
RUN apk add --no-cache \
      bash bash-completion ca-certificates curl git jq less nano openssh-client \
      mariadb-client postgresql18-client valkey-cli \
 && addgroup -g 1000 mw \
 && adduser -D -u 1000 -G mw -h /home/mw -s /bin/bash mw
COPY mw /usr/local/bin/mw
# A shell that says where it is, completes `mw`, and — in the Cloud Shell
# (MW_CLOUD_SHELL=1) — says who it is signed in as and that the disk is
# temporary.
RUN printf '%s\n' \
      '[ -n "$BASH_VERSION" ] || return 0' \
      'export PS1="\[\e[1;35m\]mw\[\e[0m\] \w \$ "' \
      'source <(mw completion bash) 2>/dev/null' \
      'if [ -n "$MW_CLOUD_SHELL" ] && [ -z "$MW_WELCOMED" ]; then' \
      '  export MW_WELCOMED=1' \
      '  echo "Cloud Shell — mw is signed in to your account (mw whoami). Files here are temporary."' \
      '  echo "Try: mw instances · mw projects · mw help"' \
      'fi' > /etc/profile.d/mw.sh \
 && printf '%s\n' '. /etc/profile.d/mw.sh' > /home/mw/.bashrc \
 && printf '%s\n' '. ~/.bashrc' > /home/mw/.bash_profile \
 && chown mw:mw /home/mw/.bashrc /home/mw/.bash_profile
ENV HOME=/home/mw
WORKDIR /home/mw
USER 1000:1000
ENTRYPOINT ["mw"]
