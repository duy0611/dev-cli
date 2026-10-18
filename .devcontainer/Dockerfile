FROM mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04

ARG MISE_VERSION=v2026.10.13
RUN curl -fsSL https://mise.run | MISE_VERSION=${MISE_VERSION} MISE_INSTALL_PATH=/usr/local/bin/mise sh

# Installed into the image rather than by postCreateCommand: every container
# gets its own empty state volume, so a hook would rebuild gopls on each create.
# --system keeps the tools out of MISE_DATA_DIR, which the volume hides.
COPY mise.toml mise.devcontainer.toml /tmp/mise/
RUN cd /tmp/mise \
    && MISE_ENV=devcontainer MISE_TRUSTED_CONFIG_PATHS=/tmp/mise mise install --system \
    && rm -rf /tmp/mise /root/.cache /root/go

# Here, not in containerEnv: the devcontainer CLI hands containerEnv to docker
# run verbatim and the k8s provider copies it into a Secret, so ${PATH} would
# arrive literally. dev runs agents without a login shell, so mise activate
# never runs and only the shims make the tools visible. A system install puts
# its shims beside it, not under MISE_DATA_DIR.
ENV PATH=/var/dev-state/mise/shims:/usr/local/share/mise/shims:$PATH
