# bifrost-guardrail-plugin

A built-in [Bifrost](https://github.com/maximhq/bifrost) HTTP-transport plugin that:

1. **Injects a governance system prompt** into every inference request, and
2. **Blocks** requests whose user input matches configured regex patterns (returns HTTP 403).

It supports both the Anthropic (`/v1/messages`) and OpenAI (`/chat/completions`, `/v1/responses`) request shapes.

## Why built-in instead of a `.so` plugin

The official `maximhq/bifrost` image is **statically linked**, so Go shared-object plugins
cannot be loaded — `plugin.Open` fails with `"Dynamic loading not supported"`. The working
approach is to compile the guardrail **into the binary** as a built-in plugin and ship a
custom image. (A `.so` variant is kept under `legacy-so-plugin/` for reference; it only
works if you build a dynamically-linked Bifrost yourself.)

## What's here

| File | Purpose |
|------|---------|
| `guardrail.go` | The plugin source — a `schemas.HTTPTransportPlugin` (drop into `transports/bifrost-http/guardrail/`) |
| `wiring.patch` | Two small edits that register the plugin in Bifrost's built-in registry |
| `Makefile` | Fetch a tag, re-apply the plugin, vet, and build a custom image |

`wiring.patch` touches:

- `transports/bifrost-http/server/plugins.go` — import + a `case` in `loadBuiltinPlugin` + registration in `loadBuiltinPlugins`
- `transports/bifrost-http/lib/config.go` — import + entry in `builtinPluginNames`

## Build

```sh
# 1. clone upstream next to this repo (or point FORK_DIR at an existing clone)
git clone https://github.com/maximhq/bifrost.git ./bifrost

# 2. build a custom image for a given upstream tag
make update TAG=transports/v1.6.0 \
     FORK_DIR=./bifrost \
     SERVER=dev@your-build-host \
     IMAGE_REPO=your-org/bifrost
```

`make help` lists all targets. `make smoke` boots a throwaway container and verifies the
guardrail registers (`plugin status: guardrail - active`), blocks a matching request (403),
and lets a non-matching request through.

## Keeping up with upstream

The footprint is one new file plus two small wiring edits, kept as `wiring.patch`. When
upstream releases a new version, `make update TAG=<new-tag>` re-applies everything. If the
upstream plugin registry changed enough that the patch no longer applies, re-wire the two
files by hand and run `make regen-patch` to refresh `wiring.patch` and `guardrail.go`.

## CI: build & auto-deploy

`.github/workflows/build-deploy.yml` builds the image on a GitHub runner, pushes it to
GHCR (`ghcr.io/<org>/bifrost:<version>`), and can deploy it straight to Nomad via the
HTTP API — no SSH needed.

- **Build only**: run the workflow via *Actions → build-and-deploy → Run workflow* with
  `deploy = false`, or just rely on it for a given upstream tag.
- **Build + deploy**: run it with `deploy = true`, or push a `v*` git tag (treated as a
  release). The deploy job runs `nomad job run -var "image=…" deploy/bifrost.nomad.hcl`.

Required repository secrets:

| Secret | Value |
|--------|-------|
| `NOMAD_ADDR` | Nomad API base URL, e.g. `https://nomad.example.com` |
| `NOMAD_TOKEN` | A Nomad ACL token allowed to submit the `bifrost` job (scope it down) |

The image carries no secrets (provider keys + `config.json` live on the Nomad host volume),
so set the GHCR package visibility to **public** to let the Nomad node pull it without auth.
`deploy/bifrost.nomad.hcl` takes the image as a variable and reads provider secrets from
Nomad Variables at deploy time.

## Enabling the plugin

The plugin is **opt-in**. Add an entry to your Bifrost `config.json`:

```json
{
  "plugins": [
    {
      "name": "guardrail",
      "enabled": true,
      "config": {
        "system_prompt": "You are a governed assistant...",
        "system_mode": "prepend",
        "block_patterns": ["(?i)some-forbidden-phrase"],
        "block_message": "Request blocked by guardrail policy."
      }
    }
  ]
}
```

| Field | Meaning |
|-------|---------|
| `system_prompt` | Text injected into every request's system prompt |
| `system_mode` | `prepend` (default — our prompt sits above the caller's) or `override` |
| `block_patterns` | Regexes; if any matches the concatenated user input, the request is blocked |
| `block_message` | Message returned with HTTP 403 on a block |

Without a `plugins` entry, the custom image behaves identically to upstream Bifrost.
