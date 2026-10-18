FROM mcr.microsoft.com/devcontainers/base:1-ubuntu-24.04

# Here, not in containerEnv: the devcontainer CLI hands containerEnv to docker
# run verbatim and the k8s provider copies it into a Secret, so ${PATH} would
# arrive literally. dev runs agents without a login shell, so mise activate
# never runs and only the shims make the tools visible.
ENV PATH=/var/dev-state/mise/shims:$PATH
