# Cluster CRD Fetch + Configurable CLI Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add `yaml-schema-router fetch` subcommand that pulls CRD schemas from a live cluster, stores them locally in the proxy's file format, and migrate the entire CLI to cobra+viper with a YAML config file.

**Architecture:** The existing proxy root command gains new flags (`--crd-schema-dir`, `--crd-fallback-remote`, and all previously-hardcoded constants). A new `fetch` subcommand uses the k8s apiextensions client to pull CRD OpenAPI schemas from the cluster and write them in proxy format (`<group>/<kind_lowercase>_<version>.json`). The CRD detector gains a pre-remote local-store lookup step.

**Tech Stack:** Go 1.25, `github.com/spf13/cobra`, `github.com/spf13/viper`, `k8s.io/apiextensions-apiserver`, `k8s.io/client-go`, `k8s.io/apimachinery`

## Global Constraints

- Module path: `go.trai.ch/yaml-schema-router`
- Root command must stay backward compatible: `yaml-schema-router --stdio` must still launch the proxy
- Local schema file format: `<output-dir>/<group>/<kind_lowercase>_<version>.json`
- Only the CRD version with `storage: true` is fetched
- Config file location: `~/.config/yaml-schema-router/config.yaml`
- Viper precedence: CLI flag > env var (`YAML_SCHEMA_ROUTER_*`) > config file > compiled default
- All tests are table-driven; no integration tests against a real cluster

---

## File Map

| File | Status | Responsibility |
|---|---|---|
| `internal/config/config.go` | **CREATE** | `ProxyConfig` and `FetchConfig` structs |
| `internal/fetcher/fetcher.go` | **CREATE** | Cluster CRD schema extraction |
| `internal/fetcher/fetcher_test.go` | **CREATE** | Fetcher unit tests (fake k8s client) |
| `internal/detector/kubernetes/crd.go` | **MODIFY** | Add `LocalSchemaDir`, `FallbackRemote`, configurable URLs |
| `internal/detector/kubernetes/crd_test.go` | **CREATE** | Local-store lookup flow tests |
| `internal/detector/kubernetes/k8s.go` | **MODIFY** | Add configurable registry/version/flavour fields |
| `internal/lspproxy/proxy.go` | **MODIFY** | Add `hover/completion/validation` fields; update `NewProxy` signature |
| `internal/lspproxy/interceptors.go` | **MODIFY** | Use `p.hover/completion/validation` instead of `config.Default*` |
| `cmd/yaml-schema-router/main.go` | **REWRITE** | cobra root + fetch subcommand + viper wiring |
| `internal/config/constants.go` | **UNCHANGED** | Remains as compiled defaults only |

---

## Task 1: Create feature branch and add config structs

**Files:**
- Create: `internal/config/config.go`

**Interfaces:**
- Produces: `config.ProxyConfig`, `config.FetchConfig` — used by Tasks 4, 5, 6

- [ ] **Step 1: Create the branch**

```bash
git checkout -b feature/cluster-crd-fetch
```

- [ ] **Step 2: Create `internal/config/config.go`**

```go
package config

import "time"

// ProxyConfig holds all runtime configuration for the LSP proxy.
type ProxyConfig struct {
	LogFile           string
	LspPath           string
	CRDSchemaDir      string
	CRDFallbackRemote bool
	K8sSchemaRegistry string
	K8sSchemaVersion  string
	K8sSchemaFlavour  string
	CRDSchemaRegistry string
	DownloadTimeout   time.Duration
	Hover             bool
	Completion        bool
	Validation        bool
}

// FetchConfig holds all runtime configuration for the fetch subcommand.
type FetchConfig struct {
	Kubeconfig string
	OutputDir  string
	All        bool
	CRDName    string
}

// DefaultProxyConfig returns a ProxyConfig populated from compiled constants.
func DefaultProxyConfig() ProxyConfig {
	return ProxyConfig{
		LspPath:           "yaml-language-server",
		CRDFallbackRemote: true,
		K8sSchemaRegistry: DefaultK8sSchemaRegistry,
		K8sSchemaVersion:  DefaultK8sSchemaVersion,
		K8sSchemaFlavour:  DefaultK8sSchemaFlavour,
		CRDSchemaRegistry: DefaultCRDSchemaRegistry,
		DownloadTimeout:   DefaultDownloaderTimeout,
		Hover:             DefaultHover,
		Completion:        DefaultCompletion,
		Validation:        DefaultValidation,
	}
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./internal/config/...
```

Expected: no output, exit 0.

- [ ] **Step 4: Commit**

```bash
git add internal/config/config.go
git commit -m "feat: add ProxyConfig and FetchConfig structs"
```

---

## Task 2: Add dependencies

**Files:**
- Modify: `go.mod`, `go.sum`

**Interfaces:**
- Produces: cobra, viper, k8s client packages available for import

- [ ] **Step 1: Add cobra and viper**

```bash
go get github.com/spf13/cobra@latest
go get github.com/spf13/viper@latest
```

- [ ] **Step 2: Add k8s client libraries**

```bash
go get k8s.io/apiextensions-apiserver@latest
go get k8s.io/client-go@latest
go get k8s.io/apimachinery@latest
```

- [ ] **Step 3: Tidy**

```bash
go mod tidy
```

- [ ] **Step 4: Verify build**

```bash
go build ./...
```

Expected: no errors (main.go still uses stdlib flag — that's fine until Task 6).

- [ ] **Step 5: Commit**

```bash
git add go.mod go.sum
git commit -m "chore: add cobra, viper, k8s client dependencies"
```

---

## Task 3: Fetcher package (TDD)

**Files:**
- Create: `internal/fetcher/fetcher.go`
- Create: `internal/fetcher/fetcher_test.go`

**Interfaces:**
- Consumes: `config.FetchConfig` from Task 1
- Produces: `fetcher.Run(cfg config.FetchConfig) error` — called by fetch subcommand in Task 6

- [ ] **Step 1: Write the failing tests**

Create `internal/fetcher/fetcher_test.go`:

```go
package fetcher_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	fakeclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"go.trai.ch/yaml-schema-router/internal/config"
	"go.trai.ch/yaml-schema-router/internal/fetcher"
)

func makeCRD(group, kind, version string, storage bool, withSchema bool) apiextensionsv1.CustomResourceDefinition {
	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: kind + "." + group},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: group,
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: kind},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{
					Name:    version,
					Storage: storage,
				},
			},
		},
	}
	if withSchema {
		crd.Spec.Versions[0].Schema = &apiextensionsv1.CustomResourceValidation{
			OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{
				Properties: map[string]apiextensionsv1.JSONSchemaProps{
					"spec":   {Type: "object"},
					"status": {Type: "object"},
				},
			},
		}
	}
	return crd
}

func TestRunWritesStorageVersionOnly(t *testing.T) {
	outDir := t.TempDir()

	crd := apiextensionsv1.CustomResourceDefinition{
		ObjectMeta: metav1.ObjectMeta{Name: "widgets.example.com"},
		Spec: apiextensionsv1.CustomResourceDefinitionSpec{
			Group: "example.com",
			Names: apiextensionsv1.CustomResourceDefinitionNames{Kind: "Widget"},
			Versions: []apiextensionsv1.CustomResourceDefinitionVersion{
				{Name: "v1beta1", Storage: false, Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object"}}},
				}},
				{Name: "v1", Storage: true, Schema: &apiextensionsv1.CustomResourceValidation{
					OpenAPIV3Schema: &apiextensionsv1.JSONSchemaProps{Properties: map[string]apiextensionsv1.JSONSchemaProps{"spec": {Type: "object"}}},
				}},
			},
		},
	}

	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, All: true}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Only the storage version file should exist
	wantPath := filepath.Join(outDir, "example.com", "widget_v1.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected file %s, got error: %v", wantPath, err)
	}

	badPath := filepath.Join(outDir, "example.com", "widget_v1beta1.json")
	if _, err := os.Stat(badPath); err == nil {
		t.Errorf("non-storage version file should not exist: %s", badPath)
	}
}

func TestRunSkipsCRDWithNoSchema(t *testing.T) {
	outDir := t.TempDir()
	crd := makeCRD("example.com", "Gadget", "v1", true, false)
	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, All: true}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(outDir, "**", "*.json"))
	if len(matches) != 0 {
		t.Errorf("expected no files, got: %v", matches)
	}
}

func TestRunFiltersByCRDName(t *testing.T) {
	outDir := t.TempDir()
	crd1 := makeCRD("example.com", "Widget", "v1", true, true)
	crd2 := makeCRD("example.com", "Gadget", "v1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd1, &crd2)
	cfg := config.FetchConfig{OutputDir: outDir, CRDName: "Widget"}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := os.Stat(filepath.Join(outDir, "example.com", "widget_v1.json")); err != nil {
		t.Errorf("expected widget file: %v", err)
	}
	if _, err := os.Stat(filepath.Join(outDir, "example.com", "gadget_v1.json")); err == nil {
		t.Error("gadget file should not exist when filtering by Widget")
	}
}

func TestRunFilenameIsLowercaseKindUnderscoreVersion(t *testing.T) {
	outDir := t.TempDir()
	crd := makeCRD("cilium.io", "CiliumBGPClusterConfig", "v2alpha1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, All: true}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wantPath := filepath.Join(outDir, "cilium.io", "ciliumbgpclusterconfig_v2alpha1.json")
	if _, err := os.Stat(wantPath); err != nil {
		t.Errorf("expected file %s: %v", wantPath, err)
	}
}

func TestRunWritesValidJSON(t *testing.T) {
	outDir := t.TempDir()
	crd := makeCRD("example.com", "Widget", "v1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, All: true}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(outDir, "example.com", "widget_v1.json"))
	if err != nil {
		t.Fatalf("could not read file: %v", err)
	}
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Errorf("file is not valid JSON: %v", err)
	}
	if schema["title"] != "Widget" {
		t.Errorf("expected title=Widget, got %v", schema["title"])
	}
}
```

- [ ] **Step 2: Run the tests to verify they fail**

```bash
go test ./internal/fetcher/... -v
```

Expected: compilation error — `fetcher` package does not exist yet.

- [ ] **Step 3: Create `internal/fetcher/fetcher.go`**

```go
package fetcher

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/clientcmd"

	"go.trai.ch/yaml-schema-router/internal/config"
)

// CRDLister is satisfied by the real and fake k8s CRD client.
type CRDLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*apiextensionsv1.CustomResourceDefinitionList, error)
}

// Run connects to the cluster and writes schemas to cfg.OutputDir.
func Run(cfg config.FetchConfig) error {
	restCfg, err := clientcmd.BuildConfigFromFlags("", cfg.Kubeconfig)
	if err != nil {
		return fmt.Errorf("kubeconfig %q: %w", cfg.Kubeconfig, err)
	}
	cs, err := apiextensionsclient.NewForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("k8s client: %w", err)
	}
	return RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions())
}

// RunWithClient is the testable core: accepts an injected CRDLister.
func RunWithClient(ctx context.Context, cfg config.FetchConfig, client CRDLister) error {
	list, err := client.List(ctx, metav1.ListOptions{})
	if err != nil {
		return fmt.Errorf("listing CRDs: %w", err)
	}

	items := list.Items
	if cfg.CRDName != "" && !cfg.All {
		items = filterByName(items, cfg.CRDName)
	}

	written := 0
	for _, crd := range items {
		if err := writeCRD(cfg.OutputDir, crd); err != nil {
			fmt.Fprintf(os.Stderr, "skipping %s: %v\n", crd.Name, err)
			continue
		}
		written++
	}

	fmt.Printf("Done. %d schema(s) written to %s\n", written, cfg.OutputDir)
	return nil
}

func filterByName(items []apiextensionsv1.CustomResourceDefinition, name string) []apiextensionsv1.CustomResourceDefinition {
	var out []apiextensionsv1.CustomResourceDefinition
	for _, crd := range items {
		if crd.Name == name || strings.EqualFold(crd.Spec.Names.Kind, name) {
			out = append(out, crd)
		}
	}
	return out
}

func writeCRD(outputDir string, crd apiextensionsv1.CustomResourceDefinition) error {
	var storageVer *apiextensionsv1.CustomResourceDefinitionVersion
	for i := range crd.Spec.Versions {
		if crd.Spec.Versions[i].Storage {
			storageVer = &crd.Spec.Versions[i]
			break
		}
	}
	if storageVer == nil {
		return fmt.Errorf("no storage version found")
	}
	if storageVer.Schema == nil || storageVer.Schema.OpenAPIV3Schema == nil {
		return fmt.Errorf("storage version %s has no OpenAPIV3Schema", storageVer.Name)
	}

	schema := map[string]any{
		"$schema": "http://json-schema.org",
		"title":   crd.Spec.Names.Kind,
		"type":    "object",
		"properties": map[string]any{
			"apiVersion": map[string]string{"type": "string"},
			"kind":       map[string]string{"type": "string"},
			"metadata":   map[string]string{"type": "object"},
			"spec":       storageVer.Schema.OpenAPIV3Schema.Properties["spec"],
			"status":     storageVer.Schema.OpenAPIV3Schema.Properties["status"],
		},
		"required": []string{"apiVersion", "kind", "metadata", "spec"},
	}

	data, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		return err
	}

	kindLower := strings.ToLower(crd.Spec.Names.Kind)
	fileName := fmt.Sprintf("%s_%s.json", kindLower, storageVer.Name)
	dir := filepath.Join(outputDir, crd.Spec.Group)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}

	fmt.Printf("Fetched: %s/%s\n", crd.Spec.Group, fileName)
	return nil
}
```

- [ ] **Step 4: Run the tests**

```bash
go test ./internal/fetcher/... -v
```

Expected: all 5 tests PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/fetcher/
git commit -m "feat: add fetcher package for cluster CRD schema extraction"
```

---

## Task 4: Update CRD and K8s detectors

Make previously-hardcoded constants runtime-configurable on the detector structs, and add local-store lookup to `CRDDetector`.

**Files:**
- Modify: `internal/detector/kubernetes/crd.go`
- Modify: `internal/detector/kubernetes/k8s.go`
- Create: `internal/detector/kubernetes/crd_test.go`

**Interfaces:**
- Consumes: `config.ProxyConfig` fields (passed in from `main.go` in Task 6)
- Produces:
  - `kubernetes.CRDDetector{Registry, CRDSchemaRegistryURL, K8sSchemaRegistryURL, K8sSchemaVersion, K8sSchemaFlavour, LocalSchemaDir, FallbackRemote}`
  - `kubernetes.K8sDetector{Registry, SchemaRegistryURL, SchemaVersion, SchemaFlavour}`

- [ ] **Step 1: Write failing tests for the new CRDDetector lookup flow**

Create `internal/detector/kubernetes/crd_test.go`:

```go
package kubernetes_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"go.trai.ch/yaml-schema-router/internal/config"
	"go.trai.ch/yaml-schema-router/internal/detector/kubernetes"
	"go.trai.ch/yaml-schema-router/internal/schemaregistry"
)

const minimalCRDYAML = `apiVersion: cilium.io/v2alpha1
kind: CiliumBGPClusterConfig
metadata:
  name: test
`

// buildRegistry creates a real registry pointing at a temp dir.
func buildRegistry(t *testing.T) *schemaregistry.Registry {
	t.Helper()
	reg, err := schemaregistry.NewRegistryAt(t.TempDir())
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	return reg
}

// seedObjectMeta writes a fake ObjectMeta schema into the registry cache
// so tests that reach fetchDependencies don't require network access.
func seedObjectMeta(t *testing.T, reg *schemaregistry.Registry, version, flavour string) {
	t.Helper()
	versionDir := version + flavour
	cachePath := filepath.Join(kubernetes.K8sDetectorName, versionDir, config.DefaultK8sMetaSchemaFileName)
	if err := reg.SaveLocalSchema(cachePath, []byte(`{"type":"object"}`)); err != nil {
		t.Fatalf("seedObjectMeta: %v", err)
	}
}

// seedLocalStore writes a fake base CRD schema into the local store directory.
func seedLocalStore(t *testing.T, localDir, group, filename string) {
	t.Helper()
	dir := filepath.Join(localDir, group)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte(`{"type":"object","properties":{"spec":{"type":"object"}}}`)
	if err := os.WriteFile(filepath.Join(dir, filename), content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestCRDDetectorLocalStoreHit(t *testing.T) {
	localDir := t.TempDir()
	reg := buildRegistry(t)
	seedLocalStore(t, localDir, "cilium.io", "ciliumbgpclusterconfig_v2alpha1.json")
	// ObjectMeta is also needed — seed it so no network call is made
	seedObjectMeta(t, reg, "v1.33.0", "-standalone-strict")

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       localDir,
		FallbackRemote:       false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(minimalCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Fatal("expected at least one schema URL, got none")
	}
}

func TestCRDDetectorLocalStoreMissFallbackDisabled(t *testing.T) {
	reg := buildRegistry(t)

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       t.TempDir(), // empty — no schemas
		FallbackRemote:       false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(minimalCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("expected no URLs when local miss and fallback disabled, got: %v", urls)
	}
}

func TestCRDDetectorNoLocalStoreUsesRemote(t *testing.T) {
	reg := buildRegistry(t)
	seedObjectMeta(t, reg, "v1.33.0", "-standalone-strict")

	// Serve a minimal schema from a local httptest server so there's no real network call.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"object","properties":{"spec":{"type":"object"}}}`))
	}))
	defer srv.Close()

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: srv.URL,
		K8sSchemaRegistryURL: srv.URL,
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       "", // disabled — should go straight to remote
		FallbackRemote:       true,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(minimalCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Error("expected a schema URL from the remote server")
	}
}

func TestCRDDetectorWrapperCacheHitSkipsEverything(t *testing.T) {
	reg := buildRegistry(t)

	// Pre-seed the wrapper directly into the registry cache
	wrapperPath := filepath.Join(kubernetes.CRDDetectorName, "cilium.io", "ciliumbgpclusterconfig_v2alpha1_wrapper.json")
	if err := reg.SaveLocalSchema(wrapperPath, []byte(`{"allOf":[]}`)); err != nil {
		t.Fatal(err)
	}

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       "",
		FallbackRemote:       false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(minimalCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Error("expected schema URL from cached wrapper")
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

```bash
go test ./internal/detector/kubernetes/... -v
```

Expected: compilation errors — `NewRegistryAt` does not exist, `CRDDetector` fields don't match.

- [ ] **Step 3: Add `NewRegistryAt` to `internal/schemaregistry/registry.go`**

Add this function after `NewRegistry`:

```go
// NewRegistryAt initializes a registry with an explicit base directory.
// Useful for testing without touching the user's real cache.
func NewRegistryAt(baseDir string) (*Registry, error) {
	if err := os.MkdirAll(baseDir, config.DefaultDirPerm); err != nil {
		return nil, fmt.Errorf("could not create cache dir: %w", err)
	}
	return &Registry{baseDir: baseDir}, nil
}
```

- [ ] **Step 4: Update `internal/detector/kubernetes/crd.go`**

Replace the struct and `fetchDependencies` method. Full file:

```go
package kubernetes

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"go.trai.ch/yaml-schema-router/internal/config"
	"go.trai.ch/yaml-schema-router/internal/detector"
	"go.trai.ch/yaml-schema-router/internal/schemaregistry"
)

type schemaRef struct {
	Ref string `json:"$ref"`
}

type schemaProperties struct {
	Metadata schemaRef `json:"metadata"`
}

type schemaExtension struct {
	Properties schemaProperties `json:"properties"`
}

type schemaWrapper struct {
	AllOf []any `json:"allOf"`
}

// CRDDetector implements the detector.Detector interface for Kubernetes CRDs.
type CRDDetector struct {
	Registry             *schemaregistry.Registry
	CRDSchemaRegistryURL string
	K8sSchemaRegistryURL string
	K8sSchemaVersion     string
	K8sSchemaFlavour     string
	LocalSchemaDir       string
	FallbackRemote       bool
}

var _ detector.Detector = (*CRDDetector)(nil)

const CRDDetectorName = "kubernetes-crd"

func (d *CRDDetector) Name() string { return CRDDetectorName }

// Detect inspects the YAML content for apiVersions containing custom groups
// and constructs wrapped JSON schemas that include standard ObjectMeta.
func (d *CRDDetector) Detect(_ string, content []byte) ([]string, error) {
	metas := extractAllTypeMeta(content)
	if len(metas) == 0 {
		return nil, nil
	}

	schemaURLs := make([]string, 0, len(metas))

	for _, meta := range metas {
		group, version, found := strings.Cut(meta.APIVersion, "/")
		if !found || (!strings.Contains(group, ".") || strings.HasSuffix(group, "k8s.io")) {
			continue
		}

		log.Printf("[%s] Detected Custom Resource: %s/%s", d.Name(), group, meta.Kind)

		kindFormatted := strings.ToLower(meta.Kind)
		fileName := fmt.Sprintf("%s_%s.json", kindFormatted, version)
		wrapperCachePath := filepath.Join(CRDDetectorName, group, fmt.Sprintf("%s_%s_wrapper.json", kindFormatted, version))

		if _, statErr := os.Stat(d.Registry.GetLocalPath(wrapperCachePath)); statErr == nil {
			log.Printf("[%s] Wrapper cache hit for %s", d.Name(), wrapperCachePath)
			schemaURLs = append(schemaURLs, d.Registry.GetLocalFileURI(wrapperCachePath))
			continue
		}

		log.Printf("[%s] Wrapper cache miss. Fetching dependencies...", d.Name())

		localBaseCRDURI, localObjectMetaURI, err := d.fetchDependencies(group, fileName)
		if err != nil {
			log.Printf("[%s] Failed to fetch dependencies for CRD %s: %v", d.Name(), meta.Kind, err)
			continue
		}

		fileURI, err := d.generateAndSaveWrapper(localBaseCRDURI, localObjectMetaURI, wrapperCachePath)
		if err != nil {
			log.Printf("[%s] Failed to generate wrapper for CRD %s: %v", d.Name(), meta.Kind, err)
			continue
		}

		schemaURLs = append(schemaURLs, fileURI)
	}

	return schemaURLs, nil
}

func (d *CRDDetector) fetchDependencies(group, fileName string) (localBaseCRDURI, localObjectMetaURI string, err error) {
	localBaseCRDURI, err = d.resolveBaseCRDSchema(group, fileName)
	if err != nil {
		return "", "", fmt.Errorf("base CRD schema: %w", err)
	}

	versionDir := fmt.Sprintf("%s%s", d.K8sSchemaVersion, d.K8sSchemaFlavour)
	objectMetaURL, err := url.JoinPath(d.K8sSchemaRegistryURL, versionDir, config.DefaultK8sMetaSchemaFileName)
	if err != nil {
		return "", "", err
	}
	metaCachePath := filepath.Join(K8sDetectorName, versionDir, config.DefaultK8sMetaSchemaFileName)
	localObjectMetaURI, err = d.Registry.GetSchemaURI(objectMetaURL, metaCachePath)
	if err != nil {
		return "", "", fmt.Errorf("ObjectMeta schema: %w", err)
	}

	return localBaseCRDURI, localObjectMetaURI, nil
}

func (d *CRDDetector) resolveBaseCRDSchema(group, fileName string) (string, error) {
	cachePath := filepath.Join(d.Name(), group, fileName)

	// 1. Check local store
	if d.LocalSchemaDir != "" {
		localPath := filepath.Join(d.LocalSchemaDir, group, fileName)
		if data, readErr := os.ReadFile(localPath); readErr == nil {
			log.Printf("[%s] Local store hit: %s", d.Name(), localPath)
			if saveErr := d.Registry.SaveLocalSchema(cachePath, data); saveErr != nil {
				return "", fmt.Errorf("cache local schema: %w", saveErr)
			}
			return d.Registry.GetLocalFileURI(cachePath), nil
		}
	}

	// 2. Fall back to remote if enabled
	if !d.FallbackRemote {
		return "", fmt.Errorf("schema %s/%s not in local store and remote fallback is disabled", group, fileName)
	}

	remoteURL, err := url.JoinPath(d.CRDSchemaRegistryURL, group, fileName)
	if err != nil {
		return "", err
	}
	return d.Registry.GetSchemaURI(remoteURL, cachePath)
}

func (d *CRDDetector) generateAndSaveWrapper(localBaseCRDURI, localObjectMetaURI, wrapperCachePath string) (string, error) {
	log.Printf("[%s] Generating schema wrapper: %s + %s -> %s",
		d.Name(), localBaseCRDURI, localObjectMetaURI, wrapperCachePath)

	wrapper := schemaWrapper{
		AllOf: []any{
			schemaRef{Ref: localBaseCRDURI},
			schemaExtension{
				Properties: schemaProperties{
					Metadata: schemaRef{Ref: localObjectMetaURI},
				},
			},
		},
	}

	wrapperBytes, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return "", err
	}

	if err := d.Registry.SaveLocalSchema(wrapperCachePath, wrapperBytes); err != nil {
		return "", err
	}

	return d.Registry.GetLocalFileURI(wrapperCachePath), nil
}
```

- [ ] **Step 5: Update `internal/detector/kubernetes/k8s.go`** — add configurable fields

Replace the `K8sDetector` struct and `resolveSchemaURL` method. Only the struct definition and `resolveSchemaURL` change; `Detect`, `Name`, and `extractAllTypeMeta` are unchanged.

```go
// K8sDetector implements the detector.Detector interface for Kubernetes manifests.
type K8sDetector struct {
	Registry          *schemaregistry.Registry
	SchemaRegistryURL string
	SchemaVersion     string
	SchemaFlavour     string
}
```

In `resolveSchemaURL`, replace all three `config.Default*` references:
- `config.DefaultK8sSchemaRegistry` → `d.SchemaRegistryURL`
- `config.DefaultK8sSchemaVersion` → `d.SchemaVersion`  
- `config.DefaultK8sSchemaFlavour` → `d.SchemaFlavour`

The full updated `resolveSchemaURL`:

```go
func (d *K8sDetector) resolveSchemaURL(meta typeMeta) string {
	log.Printf("[%s] Found apiVersion='%s', kind='%s'", d.Name(), meta.APIVersion, meta.Kind)

	if meta.Kind == "CustomResourceDefinition" {
		log.Printf("[%s] Ignoring CustomResourceDefinition", d.Name())
		return ""
	}

	group := meta.APIVersion
	version := ""
	if strings.Contains(group, "/") {
		parts := strings.Split(group, "/")
		group = parts[0]
		version = parts[1]
	}

	if strings.Contains(group, ".") && !strings.HasSuffix(group, "k8s.io") {
		log.Printf("[%s] Ignoring Custom Resource (group: %s)", d.Name(), group)
		return ""
	}

	formattedGroup := group
	if strings.Contains(formattedGroup, ".") && strings.HasSuffix(formattedGroup, "k8s.io") {
		formattedGroup = strings.Split(formattedGroup, ".")[0]
	}

	var apiVersionFormatted string
	if version != "" {
		apiVersionFormatted = fmt.Sprintf("%s-%s", formattedGroup, version)
	} else {
		apiVersionFormatted = formattedGroup
	}

	kindFormatted := strings.ToLower(meta.Kind)
	fileName := fmt.Sprintf("%s-%s.json", kindFormatted, apiVersionFormatted)
	versionDir := fmt.Sprintf("%s%s", d.SchemaVersion, d.SchemaFlavour)

	remoteSchemaURL, err := url.JoinPath(d.SchemaRegistryURL, versionDir, fileName)
	if err != nil {
		log.Printf("[%s] Failed to build URL for %s: %v", d.Name(), meta.Kind, err)
		return ""
	}

	cachePath := filepath.Join(d.Name(), versionDir, fileName)
	localURI, err := d.Registry.GetSchemaURI(remoteSchemaURL, cachePath)
	if err != nil {
		log.Printf("[%s] Failed to fetch schema for %s: %v", d.Name(), meta.Kind, err)
		return ""
	}

	return localURI
}
```

- [ ] **Step 6: Run the tests**

```bash
go test ./internal/detector/kubernetes/... -v
```

Expected: all 3 CRD detector tests PASS.

- [ ] **Step 7: Verify the full build still compiles**

```bash
go build ./...
```

Expected: exit 0. (`main.go` will fail here because it still uses the old struct literals — fix below.)

If `main.go` fails to compile due to changed struct fields, temporarily update the struct literals in `main.go` to compile (the full rewrite happens in Task 6):

```go
k8sDetector := &kubernetes.K8sDetector{
    Registry:          registry,
    SchemaRegistryURL: config.DefaultK8sSchemaRegistry,
    SchemaVersion:     config.DefaultK8sSchemaVersion,
    SchemaFlavour:     config.DefaultK8sSchemaFlavour,
}
crdDetector := &kubernetes.CRDDetector{
    Registry:             registry,
    CRDSchemaRegistryURL: config.DefaultCRDSchemaRegistry,
    K8sSchemaRegistryURL: config.DefaultK8sSchemaRegistry,
    K8sSchemaVersion:     config.DefaultK8sSchemaVersion,
    K8sSchemaFlavour:     config.DefaultK8sSchemaFlavour,
    FallbackRemote:       true,
}
```

- [ ] **Step 8: Commit**

```bash
git add internal/schemaregistry/registry.go \
        internal/detector/kubernetes/crd.go \
        internal/detector/kubernetes/k8s.go \
        internal/detector/kubernetes/crd_test.go
git commit -m "feat: configurable detector URLs and local-store CRD lookup"
```

---

## Task 5: Make lspproxy feature flags configurable

Replace the hardcoded `config.DefaultHover/Completion/Validation` constants in `interceptors.go` with instance fields on the `Proxy` struct.

**Files:**
- Modify: `internal/lspproxy/proxy.go`
- Modify: `internal/lspproxy/interceptors.go`

**Interfaces:**
- Consumes: `config.ProxyConfig.Hover`, `.Completion`, `.Validation`
- Produces: `lspproxy.NewProxy(lspPath string, chain *detector.Chain, registry *schemaregistry.Registry, cfg config.ProxyConfig) *Proxy`

- [ ] **Step 1: Add feature flag fields to `Proxy` struct in `proxy.go`**

In the `Proxy` struct, add three fields after `stateMutex`:

```go
hover      bool
completion bool
validation bool
```

Update `NewProxy` to accept `cfg config.ProxyConfig` and populate them:

```go
func NewProxy(lspPath string, chain *detector.Chain, registry *schemaregistry.Registry, cfg config.ProxyConfig) *Proxy {
	return &Proxy{
		editorIn:      os.Stdin,
		editorOut:     os.Stdout,
		lspPath:       lspPath,
		detectorChain: chain,
		registry:      registry,
		schemaState:   make(map[string]string),
		hover:         cfg.Hover,
		completion:    cfg.Completion,
		validation:    cfg.Validation,
	}
}
```

- [ ] **Step 2: Update `injectFeatureDefaults` in `interceptors.go`**

Change `injectFeatureDefaults` from a package-level function to a method on `*Proxy`, removing the `config` import dependency:

```go
func (p *Proxy) injectFeatureDefaults(yamlConfig map[string]any) bool {
	modified := false
	featureDefaults := map[string]bool{
		"hover":      p.hover,
		"completion": p.completion,
		"validation": p.validation,
	}
	for key, defaultValue := range featureDefaults {
		if _, exists := yamlConfig[key]; !exists {
			yamlConfig[key] = defaultValue
			modified = true
		}
	}
	return modified
}
```

Update the call site in `interceptWorkspaceConfiguration` from `injectFeatureDefaults(yamlConfig)` to `p.injectFeatureDefaults(yamlConfig)`.

Remove the `"go.trai.ch/yaml-schema-router/internal/config"` import from `interceptors.go` if it is no longer referenced.

- [ ] **Step 3: Fix the `main.go` temporary stub from Task 4**

Update the `NewProxy` call to pass a minimal `ProxyConfig`:

```go
proxy := lspproxy.NewProxy(*lspPath, chain, registry, config.DefaultProxyConfig())
```

- [ ] **Step 4: Verify build and tests**

```bash
go build ./...
go test ./...
```

Expected: build succeeds, all existing tests pass.

- [ ] **Step 5: Commit**

```bash
git add internal/lspproxy/proxy.go internal/lspproxy/interceptors.go cmd/yaml-schema-router/main.go
git commit -m "feat: make lspproxy feature flags runtime-configurable"
```

---

## Task 6: Rewrite main.go with cobra + viper

**Files:**
- Rewrite: `cmd/yaml-schema-router/main.go`

**Interfaces:**
- Consumes: `config.ProxyConfig`, `config.FetchConfig`, `config.DefaultProxyConfig()`
- Consumes: `fetcher.Run(cfg config.FetchConfig) error`
- Consumes: `kubernetes.K8sDetector{...}`, `kubernetes.CRDDetector{...}`
- Consumes: `lspproxy.NewProxy(lspPath, chain, registry, cfg config.ProxyConfig)`

- [ ] **Step 1: Rewrite `cmd/yaml-schema-router/main.go`**

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"go.trai.ch/yaml-schema-router/internal/config"
	"go.trai.ch/yaml-schema-router/internal/detector"
	"go.trai.ch/yaml-schema-router/internal/detector/kubernetes"
	"go.trai.ch/yaml-schema-router/internal/fetcher"
	"go.trai.ch/yaml-schema-router/internal/lspproxy"
	"go.trai.ch/yaml-schema-router/internal/schemaregistry"
)

const componentName = "Main"

func main() {
	if err := newRootCmd().Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCmd() *cobra.Command {
	defaults := config.DefaultProxyConfig()

	var (
		logFile           string
		lspPath           string
		crdSchemaDir      string
		crdFallbackRemote bool
		k8sSchemaRegistry string
		k8sSchemaVersion  string
		k8sSchemaFlavour  string
		crdSchemaRegistry string
		downloadTimeout   time.Duration
		hover             bool
		completion        bool
		validation        bool
	)

	root := &cobra.Command{
		Use:          "yaml-schema-router",
		Short:        "LSP proxy that routes YAML files to the correct JSON schema",
		SilenceUsage: true,
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			viper.SetEnvPrefix("YAML_SCHEMA_ROUTER")
			viper.AutomaticEnv()

			cfgFile := filepath.Join(mustUserConfigDir(), config.DefaultConfigDirName, "config.yaml")
			viper.SetConfigFile(cfgFile)
			if err := viper.ReadInConfig(); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("config file: %w", err)
			}

			// Bind all flags so viper picks up CLI overrides
			if err := viper.BindPFlags(cmd.Flags()); err != nil {
				return err
			}
			if cmd.HasParent() {
				if err := viper.BindPFlags(cmd.Parent().PersistentFlags()); err != nil {
					return err
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.ProxyConfig{
				LogFile:           viper.GetString("log-file"),
				LspPath:           viper.GetString("lsp-path"),
				CRDSchemaDir:      viper.GetString("crd-schema-dir"),
				CRDFallbackRemote: viper.GetBool("crd-fallback-remote"),
				K8sSchemaRegistry: viper.GetString("k8s-schema-registry"),
				K8sSchemaVersion:  viper.GetString("k8s-schema-version"),
				K8sSchemaFlavour:  viper.GetString("k8s-schema-flavour"),
				CRDSchemaRegistry: viper.GetString("crd-schema-registry"),
				DownloadTimeout:   viper.GetDuration("download-timeout"),
				Hover:             viper.GetBool("hover"),
				Completion:        viper.GetBool("completion"),
				Validation:        viper.GetBool("validation"),
			}
			return runProxy(cfg)
		},
	}

	homeDir, _ := os.UserHomeDir()
	defaultLog := filepath.Join(homeDir, ".config", config.DefaultConfigDirName, "router.log")

	root.PersistentFlags().StringVar(&logFile, "log-file", defaultLog, "Path to write logs")
	root.PersistentFlags().StringVar(&lspPath, "lsp-path", defaults.LspPath, "Path to yaml-language-server executable")
	root.PersistentFlags().Bool("stdio", true, "Ignored; kept for LSP client compatibility")
	root.PersistentFlags().StringVar(&crdSchemaDir, "crd-schema-dir", "", "Local CRD schema store (empty = disabled)")
	root.PersistentFlags().BoolVar(&crdFallbackRemote, "crd-fallback-remote", defaults.CRDFallbackRemote, "Fall back to remote registry on local store miss")
	root.PersistentFlags().StringVar(&k8sSchemaRegistry, "k8s-schema-registry", defaults.K8sSchemaRegistry, "Base URL for Kubernetes schemas")
	root.PersistentFlags().StringVar(&k8sSchemaVersion, "k8s-schema-version", defaults.K8sSchemaVersion, "Kubernetes schema version")
	root.PersistentFlags().StringVar(&k8sSchemaFlavour, "k8s-schema-flavour", defaults.K8sSchemaFlavour, "Kubernetes schema flavour suffix")
	root.PersistentFlags().StringVar(&crdSchemaRegistry, "crd-schema-registry", defaults.CRDSchemaRegistry, "Base URL for CRD schemas")
	root.PersistentFlags().DurationVar(&downloadTimeout, "download-timeout", defaults.DownloadTimeout, "HTTP download timeout")
	root.PersistentFlags().BoolVar(&hover, "hover", defaults.Hover, "Enable LSP hover")
	root.PersistentFlags().BoolVar(&completion, "completion", defaults.Completion, "Enable LSP completion")
	root.PersistentFlags().BoolVar(&validation, "validation", defaults.Validation, "Enable LSP validation")

	root.AddCommand(newFetchCmd())
	return root
}

func newFetchCmd() *cobra.Command {
	var (
		kubeconfig string
		outputDir  string
		all        bool
		crdName    string
	)

	homeDir, _ := os.UserHomeDir()
	defaultKubeconfig := filepath.Join(homeDir, ".kube", "config")
	defaultOutputDir := filepath.Join(homeDir, ".local", "crdschema")

	cmd := &cobra.Command{
		Use:          "fetch",
		Short:        "Download CRD schemas from the active Kubernetes cluster",
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !cmd.Flags().Changed("all") && !cmd.Flags().Changed("crd") {
				return cmd.Help()
			}
			cfg := config.FetchConfig{
				Kubeconfig: viper.GetString("kubeconfig"),
				OutputDir:  viper.GetString("output-dir"),
				All:        viper.GetBool("all"),
				CRDName:    viper.GetString("crd"),
			}
			return fetcher.Run(cfg)
		},
	}

	cmd.Flags().StringVar(&kubeconfig, "kubeconfig", defaultKubeconfig, "Path to kubeconfig file")
	cmd.Flags().StringVarP(&outputDir, "output-dir", "o", defaultOutputDir, "Directory where schemas will be written")
	cmd.Flags().BoolVar(&all, "all", false, "Download all CRDs")
	cmd.Flags().StringVar(&crdName, "crd", "", "Download a specific CRD by full name or kind")

	_ = viper.BindPFlag("kubeconfig", cmd.Flags().Lookup("kubeconfig"))
	_ = viper.BindPFlag("output-dir", cmd.Flags().Lookup("output-dir"))
	_ = viper.BindPFlag("all", cmd.Flags().Lookup("all"))
	_ = viper.BindPFlag("crd", cmd.Flags().Lookup("crd"))

	return cmd
}

func runProxy(cfg config.ProxyConfig) error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := setupLogging(cfg.LogFile); err != nil {
		return err
	}

	if cfg.CRDSchemaDir != "" {
		if _, err := os.Stat(cfg.CRDSchemaDir); os.IsNotExist(err) {
			return fmt.Errorf("--crd-schema-dir %q does not exist; run 'yaml-schema-router fetch' first", cfg.CRDSchemaDir)
		}
	}

	log.Printf("[%s] Starting yaml-schema-router. LSP: %s", componentName, cfg.LspPath)

	registry, err := schemaregistry.NewRegistry()
	if err != nil {
		return fmt.Errorf("schema registry: %w", err)
	}

	k8sDetector := &kubernetes.K8sDetector{
		Registry:          registry,
		SchemaRegistryURL: cfg.K8sSchemaRegistry,
		SchemaVersion:     cfg.K8sSchemaVersion,
		SchemaFlavour:     cfg.K8sSchemaFlavour,
	}
	crdDetector := &kubernetes.CRDDetector{
		Registry:             registry,
		CRDSchemaRegistryURL: cfg.CRDSchemaRegistry,
		K8sSchemaRegistryURL: cfg.K8sSchemaRegistry,
		K8sSchemaVersion:     cfg.K8sSchemaVersion,
		K8sSchemaFlavour:     cfg.K8sSchemaFlavour,
		LocalSchemaDir:       cfg.CRDSchemaDir,
		FallbackRemote:       cfg.CRDFallbackRemote,
	}
	chain := detector.NewChain(k8sDetector, crdDetector)
	proxy := lspproxy.NewProxy(cfg.LspPath, chain, registry, cfg)

	if err := proxy.Start(ctx); err != nil {
		return err
	}

	log.Printf("[%s] Proxy shut down cleanly.", componentName)
	return nil
}

func setupLogging(logFile string) error {
	log.SetFlags(log.Ldate | log.Ltime | log.Lmicroseconds)
	if logFile == "" {
		log.SetOutput(os.Stderr)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(logFile), config.DefaultDirPerm); err != nil {
		return err
	}
	f, err := os.OpenFile(logFile, os.O_RDWR|os.O_CREATE|os.O_APPEND, config.DefaultFilePerm)
	if err != nil {
		return err
	}
	log.SetOutput(f)
	return nil
}

func mustUserConfigDir() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		return os.TempDir()
	}
	return dir
}
```

- [ ] **Step 2: Build**

```bash
go build ./...
```

Expected: exit 0.

- [ ] **Step 3: Smoke test the help output**

```bash
go run ./cmd/yaml-schema-router --help
go run ./cmd/yaml-schema-router fetch --help
```

Expected: both print usage with all described flags. No panics.

- [ ] **Step 4: Run all tests**

```bash
go test ./...
```

Expected: all tests pass.

- [ ] **Step 5: Commit**

```bash
git add cmd/yaml-schema-router/main.go
git commit -m "feat: migrate CLI to cobra+viper with fetch subcommand"
```

---

## Task 7: Final integration check and PR

- [ ] **Step 1: Run the full test suite one more time**

```bash
go test ./... -race -count=1
```

Expected: all tests pass with race detector enabled.

- [ ] **Step 2: Verify the binary still accepts the legacy flag used by editor integrations**

```bash
go build -o /tmp/ysr ./cmd/yaml-schema-router && /tmp/ysr --stdio --help
```

Expected: prints help and exits 0 (the `--stdio` flag is silently accepted for compatibility).

- [ ] **Step 3: Commit and push**

```bash
git push -u origin feature/cluster-crd-fetch
```

- [ ] **Step 4: Open PR**

```bash
gh pr create \
  --title "feat: cluster CRD fetch subcommand + configurable CLI (cobra/viper)" \
  --body "$(cat docs/superpowers/specs/2026-06-30-cluster-crd-fetch-design.md)"
```
