package kubernetes_test

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/netops2devops/yaml-schema-router/internal/config"
	"github.com/netops2devops/yaml-schema-router/internal/detector/kubernetes"
	"github.com/netops2devops/yaml-schema-router/internal/schemaregistry"
)

const minimalCRDYAML = `apiVersion: cilium.io/v2alpha1
kind: CiliumBGPClusterConfig
metadata:
  name: test
`

const builtinCRDYAML = `apiVersion: apiextensions.k8s.io/v1
kind: CustomResourceDefinition
metadata:
  name: test.example.com
`

// gatewayCRDYAML represents a Gateway API object: its group ends in "k8s.io" by
// naming convention, but it's a SIG extension API shipped as a CRD, not a core
// Kubernetes type. It's never in the built-in registry.
const gatewayCRDYAML = `apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
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
		Registry:              reg,
		CRDSchemaRegistryURL:  "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL:  "http://should-not-be-called.invalid",
		K8sSchemaVersion:      "v1.33.0",
		K8sSchemaFlavour:      "-standalone-strict",
		K8sMetaSchemaFileName: config.DefaultK8sMetaSchemaFileName,
		LocalSchemaDir:        localDir,
		FallbackRemote:        false,
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
		Registry:              reg,
		CRDSchemaRegistryURL:  srv.URL,
		K8sSchemaRegistryURL:  srv.URL,
		K8sSchemaVersion:      "v1.33.0",
		K8sSchemaFlavour:      "-standalone-strict",
		K8sMetaSchemaFileName: config.DefaultK8sMetaSchemaFileName,
		LocalSchemaDir:        "", // disabled — should go straight to remote
		FallbackRemote:        true,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(minimalCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Error("expected a schema URL from the remote server")
	}
}

func TestBuiltinCRDLocalStoreHit(t *testing.T) {
	localDir := t.TempDir()
	reg := buildRegistry(t)
	seedLocalStore(t, localDir, "apiextensions.k8s.io", "customresourcedefinition_v1.json")

	d := &kubernetes.CRDDetector{
		Registry:       reg,
		LocalSchemaDir: localDir,
		FallbackRemote: false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(builtinCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Fatal("expected a schema URL for CustomResourceDefinition, got none")
	}
}

func TestBuiltinCRDNoLocalStoreReturnsNothing(t *testing.T) {
	reg := buildRegistry(t)

	d := &kubernetes.CRDDetector{
		Registry:       reg,
		LocalSchemaDir: "", // disabled
		FallbackRemote: false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(builtinCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("expected no URLs when local store is disabled, got: %v", urls)
	}
}

func TestBuiltinCRDLocalStoreMissingFileReturnsNothing(t *testing.T) {
	reg := buildRegistry(t)

	d := &kubernetes.CRDDetector{
		Registry:       reg,
		LocalSchemaDir: t.TempDir(), // exists but empty — schema file not present
		FallbackRemote: true,        // no registry URLs set, so builtin/catalog fallback can't resolve anything
	}

	urls, err := d.Detect("file:///test.yaml", []byte(builtinCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 0 {
		t.Errorf("expected no URLs when built-in schema absent from local store and no registry URLs configured, got: %v", urls)
	}
}

// TestK8sIOGroupLocalStoreHit covers the Gateway API case: its group
// ("gateway.networking.k8s.io") ends in "k8s.io" but it's a SIG extension API,
// not a core type. A schema present in the local CRD store must be served
// directly, without ever consulting the built-in registry or CRD catalog.
func TestK8sIOGroupLocalStoreHit(t *testing.T) {
	localDir := t.TempDir()
	reg := buildRegistry(t)
	seedLocalStore(t, localDir, "gateway.networking.k8s.io", "gateway_v1.json")

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       localDir,
		FallbackRemote:       false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(gatewayCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expected exactly one schema URL served directly from local store, got: %v", urls)
	}
}

// TestK8sIOGroupUsesBuiltinRegistryWhenLocalStoreMisses covers a group that
// really is core Kubernetes (e.g. "rbac.authorization.k8s.io"): when it's not
// in the local store, it should resolve from the built-in registry and never
// reach the CRD catalog.
func TestK8sIOGroupUsesBuiltinRegistryWhenLocalStoreMisses(t *testing.T) {
	reg := buildRegistry(t)

	builtinSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"object"}`))
	}))
	defer builtinSrv.Close()

	d := &kubernetes.CRDDetector{
		Registry:             reg,
		CRDSchemaRegistryURL: "http://should-not-be-called.invalid",
		K8sSchemaRegistryURL: builtinSrv.URL,
		K8sSchemaVersion:     "v1.33.0",
		K8sSchemaFlavour:     "-standalone-strict",
		LocalSchemaDir:       "", // disabled — force straight past the local-store check
		FallbackRemote:       true,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(gatewayCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expected one schema URL from the built-in registry, got: %v", urls)
	}
}

// TestK8sIOGroupFallsBackToCRDCatalogOnBuiltinMiss covers the Gateway API case
// when nothing is cached locally: the built-in registry 404s (it's not a core
// type), so it must fall back to the CRD catalog + ObjectMeta wrapper flow,
// same as any other custom resource.
func TestK8sIOGroupFallsBackToCRDCatalogOnBuiltinMiss(t *testing.T) {
	reg := buildRegistry(t)
	seedObjectMeta(t, reg, "v1.33.0", "-standalone-strict")

	builtinSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer builtinSrv.Close()

	catalogSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"type":"object","properties":{"spec":{"type":"object"}}}`))
	}))
	defer catalogSrv.Close()

	d := &kubernetes.CRDDetector{
		Registry:              reg,
		CRDSchemaRegistryURL:  catalogSrv.URL,
		K8sSchemaRegistryURL:  builtinSrv.URL,
		K8sSchemaVersion:      "v1.33.0",
		K8sSchemaFlavour:      "-standalone-strict",
		K8sMetaSchemaFileName: config.DefaultK8sMetaSchemaFileName,
		LocalSchemaDir:        "", // disabled — force straight past the local-store check
		FallbackRemote:        true,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(gatewayCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) != 1 {
		t.Fatalf("expected a wrapped schema URL from the CRD catalog fallback, got: %v", urls)
	}
}

func TestBuiltinCRDCacheHitSkipsLocalStore(t *testing.T) {
	reg := buildRegistry(t)

	// Pre-seed the registry cache directly (simulates a warm cache from a prior run).
	cachePath := filepath.Join(kubernetes.CRDDetectorName, "apiextensions.k8s.io", "customresourcedefinition_v1.json")
	if err := reg.SaveLocalSchema(cachePath, []byte(`{"type":"object"}`)); err != nil {
		t.Fatal(err)
	}

	d := &kubernetes.CRDDetector{
		Registry:       reg,
		LocalSchemaDir: t.TempDir(), // empty — schema not here, but cache hit should precede this check
		FallbackRemote: false,
	}

	urls, err := d.Detect("file:///test.yaml", []byte(builtinCRDYAML))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(urls) == 0 {
		t.Error("expected schema URL from registry cache, got none")
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
