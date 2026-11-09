# Air-gapped install

KERN assumes github.com, ghcr.io, and registry access at runtime. On a
sealed network each of those needs a local stand-in. What follows is the
minimal working arrangement.

## Images

Mirror the four published images into a registry the fleet can reach:

```
ghcr.io/dvorinka/kern-api:<tag>
ghcr.io/dvorinka/kern-runner:<tag>
ghcr.io/dvorinka/kern-ubuntu:24.04   # multi-arch manifest (amd64+arm64)
```

```
docker pull ghcr.io/dvorinka/kern-ubuntu:24.04
docker tag  ghcr.io/dvorinka/kern-ubuntu:24.04 registry.internal/kern-ubuntu:24.04
docker push registry.internal/kern-ubuntu:24.04
```

Set `KERN_DEFAULT_IMAGE=registry.internal/kern-ubuntu:24.04` on the control
plane (and `KERN_ARM64_IMAGE=…:24.04-arm64` if arm64 runners exist).

## Remote actions (`uses:`)

Steps fetch `owner/repo@ref` from github.com at job time into the runner's
`_actions` cache. Two options:

- **`KERN_ACTIONS_GIT_BASE`** — point action fetches at a mirror host
  (GHES, or a git mirror of the needed actions).
- **Preseed offline** — on a connected box, run one job (or
  `KERN_ACTIONS_PRESEED`) so `_actions` fills, then copy
  `<work_root>/_actions/` into each runner's work root. A warm `_actions`
  dir means no action fetch at all.

The `_node` toolcache dir under the work root follows the same pattern:
populate it once (setup-node/setup-go runs), then copy it into the sealed
runners.

## Control plane

Compose up with the mirrored `kern-api`/`kern-runner` tags and a local
postgres. The GitHub App integration and public `public_ci` badges need
github.com; disable them or proxy the API. GITHUB_TOKEN minting fails
without GitHub — workflows relying on `${{ secrets.GITHUB_TOKEN }}` for
push-back do not work air-gapped.

## Updates

`KERN_RUNNER_UPDATE` and the release updater poll ghcr.io. Leave
self-update off (`auto_update` unset) and roll runners by redeploy.
