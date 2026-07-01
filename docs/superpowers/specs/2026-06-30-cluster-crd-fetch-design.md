# Design: Cluster CRD Fetch + Configurable CLI

**Date:** 2026-06-30
**Branch:** feature/cluster-crd-fetch

## Problem

The built-in CRD detector fetches schemas from `datreeio/CRDs-catalog`, which is an incomplete and sometimes stale snapshot. Users with custom or newer CRDs get no schema support. The solution is to allow `yaml-schema-router` to pull schemas directly from a live cluster and store them locally, pointing the proxy at that local store.

## Goals

1. Add `yaml-schema-router fetch` subcommand to download CRD schemas from a live cluster.
2. Save fetched schemas to a configurable local directory (`~/.local/crdschema` by default) in the proxy's file format.
3. Make the proxy aware of this local store via a `--crd-schema-dir` flag, with configurable fallback to datreeio.
4. Migrate the entire CLI from stdlib `flag` to `spf13/cobra` + `viper`, with YAML config file support.
5. Make all hardcoded constants in `internal/config/constants.go` runtime-configurable.

## Non-Goals

- Integration tests against a real cluster.
- Changing the existing cache directory structure under `~/Library/Caches/yaml-schema-router/`.
- Downloading multiple versions of a CRD (storage version only).

---

## Architecture

```
cmd/yaml-schema-router/
  main.go                    ← rewritten: cobra root + fetch subcommand, viper wiring

internal/config/
  constants.go               ← unchanged: compiled defaults only
  config.go                  ← NEW: ProxyConfig and FetchConfig structs

internal/detector/kubernetes/
  crd.go                     ← updated: local store lookup before remote

internal/fetcher/
  fetcher.go                 ← NEW: cluster CRD schema extraction logic

internal/schemaregistry/
  registry.go                ← unchanged
  downloader.go              ← unchanged
```

### CLI Shape

```
yaml-schema-router [flags]          # root command — runs LSP proxy (backward compatible)
yaml-schema-router fetch [flags]    # new subcommand — fetches from cluster
```

Existing editor integrations calling `yaml-schema-router --stdio` are unaffected.

---

## Components

### `internal/config/config.go` (new)

Two flat structs populated by viper at startup:

```go
type ProxyConfig struct {
    LogFile           string
    LspPath           string
    CRDSchemaDir      string        // local store path; empty = disabled
    CRDFallbackRemote bool          // fall back to datreeio on local miss
    K8sSchemaRegistry string
    K8sSchemaVersion  string
    K8sSchemaFlavour  string
    CRDSchemaRegistry string
    DownloadTimeout   time.Duration
    Hover             bool
    Completion        bool
    Validation        bool
}

type FetchConfig struct {
    Kubeconfig string
    OutputDir  string        // default: ~/.local/crdschema
    All        bool
    CRDName    string        // single CRD by full name or kind
}
```

### `internal/fetcher/fetcher.go` (new)

Adapts the logic from `github.com/netops2devops/crdschema` with two changes:

1. **Storage version only** — iterates `crd.Spec.Versions` and picks the entry where `storage: true` (not `Versions[0]`).
2. **Proxy filename format** — writes `<output-dir>/<group>/<kind_lowercase>_<version>.json` instead of `group/version/Kind.json`.

Prints per-schema progress to stdout and a summary line on completion.

### Updated `CRDDetector` (internal/detector/kubernetes/crd.go)

Two new fields on the struct:

```go
type CRDDetector struct {
    Registry      *schemaregistry.Registry
    LocalSchemaDir string   // path to local store; empty = disabled
    FallbackRemote bool
}
```

**Updated lookup flow in `Detect`:**

```
1. Wrapper in cache?
   YES → use it (no change)
   NO  ↓

2. LocalSchemaDir configured?
   YES → check <LocalSchemaDir>/<group>/<kind_lowercase>_<version>.json
         found?   → read bytes → generate wrapper → cache → use it
         missing  → go to step 3
   NO  → go to step 3

3. FallbackRemote = true?
   YES → fetch from datreeio (existing behavior)
   NO  → log warning "schema not found for <kind>, skipping" → skip
```

### `cmd/yaml-schema-router/main.go` (rewritten)

- Cobra root command runs the proxy (preserves `--stdio`, `--log-file`, `--lsp-path`).
- All `ProxyConfig` fields exposed as cobra persistent flags on root.
- `fetch` subcommand binds `FetchConfig` flags.
- Viper bound to all flags via `viper.BindPFlag`. Config file auto-discovered at `~/.config/yaml-schema-router/config.yaml`.
- `CRDDetector` instantiated with `LocalSchemaDir` and `FallbackRemote` from `ProxyConfig`.

---

## Configuration

### Viper Precedence (highest → lowest)

```
CLI flag → env var (YAML_SCHEMA_ROUTER_*) → config file → compiled default
```

### Config File Location

`~/.config/yaml-schema-router/config.yaml` — auto-discovered by viper. Silently ignored if absent.

### Example Config File

```yaml
lsp-path: yaml-language-server
crd-schema-dir: ~/.local/crdschema
crd-fallback-remote: true
k8s-schema-version: v1.33.0
k8s-schema-flavour: -standalone-strict
download-timeout: 2s
hover: true
completion: true
validation: true
```

### New Proxy Flags

| Flag | Default | Description |
|---|---|---|
| `--crd-schema-dir` | `""` (disabled) | Path to local CRD schema store |
| `--crd-fallback-remote` | `true` | Fall back to datreeio on local miss |
| `--k8s-schema-registry` | (from constants) | Base URL for k8s schemas |
| `--k8s-schema-version` | `v1.33.0` | k8s schema version |
| `--k8s-schema-flavour` | `-standalone-strict` | Schema flavour suffix |
| `--crd-schema-registry` | (from constants) | Base URL for CRD schemas |
| `--download-timeout` | `2s` | HTTP download timeout |
| `--hover` | `true` | Enable LSP hover |
| `--completion` | `true` | Enable LSP completion |
| `--validation` | `true` | Enable LSP validation |

### Fetch Subcommand Flags

| Flag | Default | Description |
|---|---|---|
| `--kubeconfig` | `~/.kube/config` | Path to kubeconfig |
| `--output-dir` / `-o` | `~/.local/crdschema` | Where to write schemas |
| `--all` | `false` | Download all CRDs |
| `--crd` | `""` | Single CRD by full name or kind |

---

## Error Handling

| Scenario | Behavior |
|---|---|
| Config file present but malformed | Fatal at startup, clear message |
| `--crd-schema-dir` set but directory absent | Fatal at proxy startup with suggestion to run `fetch` first |
| CRD missing from local store, `fallback-remote=false` | Log warning, skip that CRD, proxy continues |
| CRD missing from local store, `fallback-remote=true` | Attempt datreeio; on failure log and skip |
| Cluster unreachable during `fetch` | Fatal immediately, kubeconfig path shown |
| Single CRD write fails during `fetch` | Log error, continue to next CRD |
| kubeconfig not found | Fatal with path shown |
| Neither `--all` nor `--crd` provided to `fetch` | Print usage, exit 0 |

---

## Testing

### `internal/detector/kubernetes/crd_test.go`

Table-driven tests for the new lookup flow:

- Local store hit → wrapper generated, no remote call made
- Local store miss + `FallbackRemote=true` → remote called
- Local store miss + `FallbackRemote=false` → schema skipped, no remote call
- `LocalSchemaDir=""` (disabled) → existing remote behavior unchanged

### `internal/fetcher/fetcher_test.go`

Unit tests using a fake `apiextensions` client:

- Only the version with `storage: true` is written
- CRDs with no `OpenAPIV3Schema` are skipped
- Output filename matches proxy format (`<kind_lowercase>_<version>.json`)
- `--crd` filtering by full name and by kind both work

---

## Local Schema File Layout

Fetched schemas are written by the `fetch` command and read by the proxy's `CRDDetector`:

```
~/.local/crdschema/                              ← OutputDir (configurable)
  cilium.io/
    ciliumbgpclusterconfig_v2alpha1.json
  monitoring.coreos.com/
    prometheus_v1.json
    servicemonitor_v1.json
```

The generated wrappers (which merge the base schema with ObjectMeta) continue to be written into the existing cache at `~/Library/Caches/yaml-schema-router/schemas/kubernetes-crd/`.

---

## New Dependencies

- `github.com/spf13/cobra` — CLI framework
- `github.com/spf13/viper` — config file + flag binding
- `k8s.io/apiextensions-apiserver` — CRD client
- `k8s.io/client-go` — cluster connection via kubeconfig
- `k8s.io/apimachinery` — Kubernetes API types
