# Changelog

## [0.20.0](https://github.com/duy0611/dev-cli/compare/v0.19.0...v0.20.0) (2026-10-27)


### Features

* **agentcfg:** seed opencode.json from agents.yaml ([27155dd](https://github.com/duy0611/dev-cli/commit/27155dd6866df68c00876d0c6df153a238545627))
* **team:** add start-agent-team with dev-team dispatch, team list and team logs ([ff9ba63](https://github.com/duy0611/dev-cli/commit/ff9ba63321829e4f7d6318a3b5359bb381ddbde0))


### Bug Fixes

* **relay:** make the agent-readiness test independent of accept-queue limits ([70329db](https://github.com/duy0611/dev-cli/commit/70329db9cf9b7e9dd9a6dd5c2f8cd6c5ca839d80))
* **relay:** wait for the test agent to listen, not for its socket file ([9c5b460](https://github.com/duy0611/dev-cli/commit/9c5b46096d16577877c1979e9861dd3d205cc443))

## [0.19.0](https://github.com/duy0611/dev-cli/compare/v0.18.0...v0.19.0) (2026-10-25)


### Features

* **cli:** print the container record in config show ([599c330](https://github.com/duy0611/dev-cli/commit/599c330a6a21463d54cc0aebe702bc2f747d84c8))

## [0.18.0](https://github.com/duy0611/dev-cli/compare/v0.17.0...v0.18.0) (2026-10-20)


### Features

* **cli:** an unprotected mode, the folder live with .git writable ([dea2525](https://github.com/duy0611/dev-cli/commit/dea252517c240516fc076aabf5829737b0a7372e))
* **cli:** rebuild --mode changes a container's mode ([b037303](https://github.com/duy0611/dev-cli/commit/b0373036327d13d507ee1fa069dbcfdaa67421d5))
* **provider:** a rebuild that seeds a fresh isolated workspace ([059ee5d](https://github.com/duy0611/dev-cli/commit/059ee5d34921fc0b532fa377dbd84ea4aeca8331))
* **store:** record a container's change of mode ([dfc06d2](https://github.com/duy0611/dev-cli/commit/dfc06d2a1eeebb0f44cfd47e05bbb6c042d0bbcf))


### Bug Fixes

* **cli:** a mode change is not configuration drift ([339b617](https://github.com/duy0611/dev-cli/commit/339b61741ee43da4a4c2b1dca6f27b6ef0b0a58b))

## [0.17.0](https://github.com/duy0611/dev-cli/compare/v0.16.0...v0.17.0) (2026-10-19)


### Features

* **agent:** start claude, opencode and hermes with a first message ([803af6e](https://github.com/duy0611/dev-cli/commit/803af6e7a4e2862db44f5ee18428a5d08c8fcfd6))
* **cli:** dev diff edit and complete ([2ec25cc](https://github.com/duy0611/dev-cli/commit/2ec25cc9d0e4601633a8a64a61580aa728437b3e))
* **review:** read a guarded container's diff and carry review comments ([d379e9d](https://github.com/duy0611/dev-cli/commit/d379e9d5cb8878a73c62619f06092819eb733e55))
* **store:** keep a container's diff review draft and sent round ([701890a](https://github.com/duy0611/dev-cli/commit/701890ad8847ef2a9aed290ac019c2eecd9c953f))


### Bug Fixes

* mark diff command as experimental ([713de02](https://github.com/duy0611/dev-cli/commit/713de02cf21e28b7a391a6583af1a4804e6327c4))

## [0.16.0](https://github.com/duy0611/dev-cli/compare/v0.15.0...v0.16.0) (2026-10-18)


### Features

* **build:** speed up container creation ([7482fcf](https://github.com/duy0611/dev-cli/commit/7482fcf7155770547c13d7cab008dd4e7cc3145c))
* **cli:** local containers built for the engine's architecture ([f287319](https://github.com/duy0611/dev-cli/commit/f287319d10b9d3007198b117df9207e9580f8fb9))

## [0.15.0](https://github.com/duy0611/dev-cli/compare/v0.14.0...v0.15.0) (2026-10-18)


### Features

* **build:** switch from devcontainer features to mise for go and go tools ([8612609](https://github.com/duy0611/dev-cli/commit/8612609cd9b983c373048bbb1e4c7b97ad53dafc))
* **cli:** create a container on several folders ([895b08b](https://github.com/duy0611/dev-cli/commit/895b08b0021b2793d7e6d11b326db200397cc42b))
* **cli:** name the Herdr agent with start-agent --agent-name ([f53d110](https://github.com/duy0611/dev-cli/commit/f53d1107f8aed8bdad37241844e42f5be4769e73))


### Performance

* **dcgen:** skip the go feature's tool builds ([6781068](https://github.com/duy0611/dev-cli/commit/6781068ffb6bd65f20e0c57c2832c1636d8cee1b))

## [0.14.0](https://github.com/duy0611/dev-cli/compare/v0.13.0...v0.14.0) (2026-10-13)


### Features

* **cli:** trust github.com's host key in every started container ([e1b488e](https://github.com/duy0611/dev-cli/commit/e1b488ea3296b293297f50f3227cc39d53993130))
* **cli:** verify github.com's scanned host keys against its fingerprints ([0eb5ceb](https://github.com/duy0611/dev-cli/commit/0eb5ceb721abe25e13dfe831b9379ef2ee34c038))


### Bug Fixes

* **cli:** keep isolated work until it is merged or pushed ([cbc4d63](https://github.com/duy0611/dev-cli/commit/cbc4d63ff269234a5e2d0fe5b7556711e961a548))
* **env:** pass the git identity as GIT_AUTHOR_* and GIT_COMMITTER_* ([fe92c35](https://github.com/duy0611/dev-cli/commit/fe92c35e2abda64e0e31289db04ba6e0b5bcf761))

## [0.13.0](https://github.com/duy0611/dev-cli/compare/v0.12.0...v0.13.0) (2026-10-12)


### ⚠ BREAKING CHANGES

* **cli:** worktree create no longer takes the container flags (--generate, --tools, --from, --no-start, --no-persist-state, --agent-config, --no-agent-config, --allow-privileged); run dev container create in the checkout instead.

### Features

* **cli:** default container create's folder to the current directory ([83022c9](https://github.com/duy0611/dev-cli/commit/83022c9f5da4eba73e912c69a38c67aa189045f2))
* **cli:** guarded mode mounts .git read-only as one directory ([e2704c7](https://github.com/duy0611/dev-cli/commit/e2704c7d5cf28a7ea46d6aa1da573ae92968c38e))
* **cli:** isolated mode copies the folder into a volume the container owns ([470b418](https://github.com/duy0611/dev-cli/commit/470b418cdf3093aa6f780e40ce68bab418506561))
* **cli:** show each container's mode, and warn about legacy ones ([6f6bb7f](https://github.com/duy0611/dev-cli/commit/6f6bb7f5fd8ec80981e0e13ef1ed43d5e04bded5))
* **cli:** workspace init --from copies a workspace's settings ([9c0d402](https://github.com/duy0611/dev-cli/commit/9c0d402027ab8f496706b2d8783e2d3ca556fe34))
* **cli:** worktree create makes the checkout only ([c96e3b3](https://github.com/duy0611/dev-cli/commit/c96e3b3a13ef30f1f1f980b7c0d2d3602a9e5a66))
* **herdr:** name the agent after its container ([0f11957](https://github.com/duy0611/dev-cli/commit/0f119570bf2224efea648bd09f73b415489da3f5))
* **k8s:** default PVC size to 1Gi ([40956ba](https://github.com/duy0611/dev-cli/commit/40956ba6853a35dc95221417ff1e08324132242a))
* **store:** record a container's mode and worktree repository ([89f3353](https://github.com/duy0611/dev-cli/commit/89f335399f7b0070bf43c300dd22a3f983d15e4a))


### Bug Fixes

* **cli:** trust the mounted repository inside the container ([022f899](https://github.com/duy0611/dev-cli/commit/022f8999b8baf79312c96bf123a28a96e641fe80))
* **local:** seed an isolated container as the remote user, with no git guard ([73cbfcd](https://github.com/duy0611/dev-cli/commit/73cbfcd2d610428497065cc25a03f9352f3b74eb))

## [0.12.0](https://github.com/duy0611/dev-cli/compare/v0.11.0...v0.12.0) (2026-10-10)


### Features

* **cli:** record relay drops in the audit log ([33ac6a1](https://github.com/duy0611/dev-cli/commit/33ac6a1ed55c73f06ee97adbdf0510dd39be81da))
* **relay:** restart a dropped relay on the same socket ([f766080](https://github.com/duy0611/dev-cli/commit/f766080fdef78b20e8c017bf897dffbf3c416772))


### Bug Fixes

* **relay:** bound a hung respawn by the budget, and record no drop for an interrupted session ([71a954d](https://github.com/duy0611/dev-cli/commit/71a954d13361c339cb0abb2282d0b5012d09ed2e))
* **relay:** remove only the socket the relay created, and stand down once replaced ([8cf8d1a](https://github.com/duy0611/dev-cli/commit/8cf8d1a114da84382d888e0dd8dd470d3f00cbcb))

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
