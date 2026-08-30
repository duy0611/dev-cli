# devcontainer-claude-setup
#
#   make lint            shellcheck, yamllint, plutil, hadolint, JSON render check
#   make test            smoke-test the base profile end to end
#   make build           build all four images
#   make build base      build one (also: k8s, cloud, full)
#   make base            same thing, without the `build` word
#   make install         run install.sh
#
# Images depend on each other (k8s and cloud are built FROM base, full FROM k8s),
# so the targets encode that. A dependency rebuild is cheap when nothing
# changed — the layer cache makes it a few seconds — and skipping it would let
# a stale base silently persist into the derived images.

SHELL := /bin/bash
.SHELLFLAGS := -eu -o pipefail -c

PROFILES   := base k8s cloud full

# podman first, docker second. The backend is a podman machine either way, but
# reaching it through the docker CLI drops to the deprecated classic builder
# when the buildx plugin is absent. `podman build` is buildah, needs no plugin,
# and writes to the same local storage, so `FROM localhost/dcx-base` still
# resolves. Override with `make build DOCKER=docker`, or DCX_RUNTIME=docker to
# override the Makefile and install.sh at once.
DOCKER     ?= $(if $(DCX_RUNTIME),$(DCX_RUNTIME),$(shell command -v podman >/dev/null 2>&1 && echo podman || echo docker))
# Fully qualified: podman resolves short names against its own search-registry
# list, docker always means docker.io. Spelling it out means both agree.
HADOLINT   ?= docker.io/hadolint/hadolint:latest
SHELLCHECK ?= shellcheck

# Everything that is a shell script, whether or not it ends in .sh.
SH_FILES := bin/dcx bin/dcclaude bin/dcws bin/dccred install.sh \
            $(wildcard lib/*.sh) \
            images/shared/install-plugins.sh images/shared/post-create.sh \
            images/shared/dcx-shim images/shared/dcx-credcheck \
            images/shared/dcx-enable-signing \
            test/render-check.sh test/smoke-base.sh \
            $(wildcard skills/*/templates/*.sh)

CONTAINERFILES := images/base/Containerfile images/k8s/Containerfile images/cloud/Containerfile

.DEFAULT_GOAL := help
.PHONY: help lint test build install install-images install-watch clean \
        $(PROFILES) $(addprefix image-,$(PROFILES)) \
        lint-shell lint-yaml lint-plist lint-docker lint-render lint-skills

help:
	@sed -n '3,9p' $(MAKEFILE_LIST) | sed 's/^# \{0,1\}//'

# --- build -------------------------------------------------------------------

# `make build base` passes two goals to make: `build` and `base`. Pick the
# profile words out of MAKECMDGOALS and build only those; with none, build all.
BUILD_SELECTION := $(filter $(PROFILES),$(MAKECMDGOALS))

build:
	@$(MAKE) --no-print-directory \
	  $(addprefix image-,$(if $(BUILD_SELECTION),$(BUILD_SELECTION),$(PROFILES)))

# When `build` is also on the command line the profile words are just its
# arguments, so they must do nothing. On their own (`make k8s`) they build.
ifneq ($(filter build,$(MAKECMDGOALS)),)
$(PROFILES):
	@:
else
$(PROFILES): %: image-%
endif

image-base:
	$(DOCKER) build -f images/base/Containerfile -t localhost/dcx-base:latest .

image-k8s: image-base
	$(DOCKER) build -f images/k8s/Containerfile -t localhost/dcx-k8s:latest .

# cloud and full share one Containerfile, parameterised on BASE: full is exactly
# k8s plus the gcloud+aws layer, so there is no second copy to drift.
image-cloud: image-base
	$(DOCKER) build -f images/cloud/Containerfile \
	  --build-arg BASE=localhost/dcx-base:latest -t localhost/dcx-cloud:latest .

image-full: image-k8s
	$(DOCKER) build -f images/cloud/Containerfile \
	  --build-arg BASE=localhost/dcx-k8s:latest -t localhost/dcx-full:latest .

# --- lint --------------------------------------------------------------------

lint: lint-shell lint-yaml lint-plist lint-render lint-skills lint-docker
	@echo "lint: all checks passed"

# -S warning, not the default: the only info-level finding is SC2015 on the
# `[ cond ] && action || true` guard, which is deliberate. Under `set -e` a bare
# `[ cond ] && action` exits the script when the condition is false, so the
# `|| true` is load-bearing rather than a mistaken if-then-else.
# -x follows the `. "$LIB/common.sh"` sources; --source-path tells it where.
lint-shell:
	@echo "==> shellcheck"
	@$(SHELLCHECK) -S warning -x --source-path=lib --source-path=. $(SH_FILES)
	@echo "==> bash -n"
	@for f in $(SH_FILES); do bash -n "$$f" || exit 1; done

lint-yaml:
	@echo "==> yamllint"
	@yamllint -d '{extends: default, rules: {line-length: {max: 100}, document-start: disable}}' \
	  rbac/*.yaml
	@echo "==> kubectl client-side validation"
	@if command -v kubectl >/dev/null 2>&1; then \
	  kubectl apply --dry-run=client -f rbac/stub-readonly.yaml >/dev/null \
	    && echo "    rbac/stub-readonly.yaml OK"; \
	else echo "    kubectl absent, skipped"; fi

lint-plist:
	@echo "==> plutil"
	@plutil -lint launchd/*.plist

# The generated devcontainer.json is what actually reaches the devcontainer
# CLI, so lint the renderer's output rather than trusting the jq by eye. Runs
# against a throwaway state dir; nothing is created in docker.
lint-render:
	@echo "==> devcontainer.json render"
	@test/render-check.sh

# Skill templates are copied verbatim into other people's projects, so a broken
# one is discovered a long way from here. The shell halves are already covered
# by SH_FILES; this checks the JSON parses and still carries the placeholder the
# skill substitutes, which is the one edit that would silently ship un-done.
lint-skills:
	@echo "==> skill templates"
	@for f in $(wildcard skills/*/templates/devcontainer.json); do \
	  jq -e . "$$f" >/dev/null || { echo "    $$f is not valid JSON"; exit 1; }; \
	  jq -e '.name == "PROJECT_NAME"' "$$f" >/dev/null \
	    || { echo "    $$f lost its PROJECT_NAME placeholder"; exit 1; }; \
	  echo "    $$f OK"; \
	done
	@for f in $(wildcard skills/*/SKILL.md); do \
	  head -1 "$$f" | grep -qx -- '---' || { echo "    $$f has no frontmatter"; exit 1; }; \
	  grep -qE '^name: ' "$$f" && grep -qE '^description: ' "$$f" \
	    || { echo "    $$f frontmatter needs name and description"; exit 1; }; \
	  echo "    $$f OK"; \
	done

# Ignored rules, all deliberate:
#   DL3007  `:latest` on FROM - these are our own locally-built images in a
#           fixed chain (k8s FROM base, full FROM k8s); a version tag would be
#           a second thing to bump on every rebuild for no safety gain
#   DL3008  unpinned apt versions - pinning them here would freeze security
#           updates for a sandbox image that is rebuilt often anyway
#   DL3064  false positive: `ARG USERNAME=node` is a username, not a secret
#   DL3066  `USER node` by name is the devcontainer convention, and post-create
#           resolves it with id(1) rather than assuming a number
#
# hadolint is not installed locally, so run it from its own image. Skipped
# rather than failed when the image cannot be fetched: a missing linter should
# not break a build on a plane.
lint-docker:
	@echo "==> hadolint"
	@if $(DOCKER) image inspect $(HADOLINT) >/dev/null 2>&1 || $(DOCKER) pull -q $(HADOLINT) >/dev/null 2>&1; then \
	  for f in $(CONTAINERFILES); do \
	    $(DOCKER) run --rm -i $(HADOLINT) hadolint \
	      --ignore DL3007 --ignore DL3008 --ignore DL3064 --ignore DL3066 - < "$$f" \
	      && echo "    $$f OK" || exit 1; \
	  done; \
	else \
	  echo "    $(HADOLINT) unavailable, skipped"; \
	fi

# --- test --------------------------------------------------------------------

test: image-base
	@./test/smoke-base.sh

# --- install -----------------------------------------------------------------

install:
	@./install.sh

install-images:
	@./install.sh --images

install-watch:
	@./install.sh --watch

# --- clean -------------------------------------------------------------------

# Only the smoke test's own instance. Real instances are yours; use dcx --rm.
clean:
	@./bin/dcx --rm dcx-smoketest 2>/dev/null || echo "clean: nothing to remove"
