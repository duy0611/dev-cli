# Changelog

## [0.11.0](https://github.com/duy0611/dev-cli/compare/v0.10.0...v0.11.0) (2026-10-10)


### Features

* **cli:** hold read-only what host git runs from the working tree ([721cec1](https://github.com/duy0611/dev-cli/commit/721cec15d2665dc2b89ea9ea23908ed352c67b72))

## [0.10.0](https://github.com/duy0611/dev-cli/compare/v0.9.0...v0.10.0) (2026-10-09)


### Features

* **cli:** keep host git from running what a container writes under .git ([5943ae5](https://github.com/duy0611/dev-cli/commit/5943ae579c33c113527505859109c089c77346cf))
* **cli:** record what dev does to containers in an audit log, read with dev audit ([bbcd5e1](https://github.com/duy0611/dev-cli/commit/bbcd5e14d7b1d43ad9ed0a283f06717f7faa7ad8))
* **cli:** refuse a configuration that asks its engine for the host unless --allow-privileged ([8eeb6f2](https://github.com/duy0611/dev-cli/commit/8eeb6f2a46b6f8c522b67fd9e23a032e0ca08aa8))
* **cli:** refuse a rebuild whose project configuration changed unless --accept-config ([b19c307](https://github.com/duy0611/dev-cli/commit/b19c307caf10c4257b59eff65f494870a71e4bdf))
* **k8s:** apply the default seccomp profile to the pod ([78cb2b3](https://github.com/duy0611/dev-cli/commit/78cb2b3b22c7a6d63facb5802297b0acc07b27a8))

## [0.9.0](https://github.com/duy0611/dev-cli/compare/v0.8.0...v0.9.0) (2026-10-08)


### Features

* **cli:** container create --from ([7ae4796](https://github.com/duy0611/dev-cli/commit/7ae4796ea7e45617925961b0a24085b1e5c6f104))
* **cli:** show config origin in container list ([4123fd2](https://github.com/duy0611/dev-cli/commit/4123fd205c7e2574d5e33b51a78ad2aef87b84b7))
* **cli:** worktree create --from ([02327bf](https://github.com/duy0611/dev-cli/commit/02327bf33bbbf615ffc696edd4dcc1d620776f6f))

## [0.8.0](https://github.com/duy0611/dev-cli/compare/v0.7.0...v0.8.0) (2026-10-05)


### Features

* **cli:** copy listed ignored files into a new worktree ([4b087f6](https://github.com/duy0611/dev-cli/commit/4b087f6dbc5635a743742dd1494e4b6317e1667e))
* **cli:** time out container list status lookups ([a3c1a44](https://github.com/duy0611/dev-cli/commit/a3c1a44206d0cd9e121fffe112bf29d26480fe3c))
* **gitwt:** list ignored, matching and tracked files ([7065e61](https://github.com/duy0611/dev-cli/commit/7065e61014ba364b4abb71e6f8971ad148808770))
* **wtseed:** select and copy ignored files into a new checkout ([f4d07a0](https://github.com/duy0611/dev-cli/commit/f4d07a0da046a2612d5038e9dc6198a6a9e34620))

## [0.7.0](https://github.com/duy0611/dev-cli/compare/v0.6.0...v0.7.0) (2026-09-29)


### Features

* override the workspace's ssh agent forwarding per command ([6f3e5b8](https://github.com/duy0611/dev-cli/commit/6f3e5b836b5a2a341bf1349deca93ef83a5ac2c0))
* override the workspace's ssh agent forwarding per command ([0eb6f70](https://github.com/duy0611/dev-cli/commit/0eb6f70b08d797ed2daf0a0e61416bf871041fa8))
