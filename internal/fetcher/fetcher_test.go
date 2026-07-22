package fetcher_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	fakeclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset/fake"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/netops2devops/yaml-schema-router/internal/config"
	"github.com/netops2devops/yaml-schema-router/internal/fetcher"
)

// minimalOpenAPIDoc is a trimmed OpenAPI v3 document that exercises cross-schema $ref
// resolution: the List schema references the CRD schema, so if the output splits root vs
// definitions incorrectly the ref would dangle.
const minimalOpenAPIDoc = `{
  "components": {
    "schemas": {
      "io.test.v1.CustomResourceDefinition": {
        "type": "object",
        "properties": {
          "spec": {"$ref": "#/components/schemas/io.test.v1.CustomResourceDefinitionSpec"}
        }
      },
      "io.test.v1.CustomResourceDefinitionSpec": {
        "type": "object"
      },
      "io.test.v1.CustomResourceDefinitionList": {
        "type": "object",
        "properties": {
          "items": {
            "type": "array",
            "items": {"$ref": "#/components/schemas/io.test.v1.CustomResourceDefinition"}
          }
        }
      }
    }
  }
}`

// mockBuiltinFetcher satisfies fetcher.BuiltinSchemaFetcher for tests.
type mockBuiltinFetcher struct {
	data []byte
	err  error
}

func (m *mockBuiltinFetcher) FetchGroupVersionOpenAPI(_ context.Context, _, _ string) ([]byte, error) {
	return m.data, m.err
}

func validBuiltinMock() *mockBuiltinFetcher {
	return &mockBuiltinFetcher{data: []byte(minimalOpenAPIDoc)}
}

func errorBuiltinMock() *mockBuiltinFetcher {
	return &mockBuiltinFetcher{err: errors.New("not available in test")}
}

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

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), validBuiltinMock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

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

	// errorBuiltinMock ensures the built-in schema fetch also produces nothing,
	// so the assertion "zero JSON files" holds end-to-end.
	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), errorBuiltinMock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var matches []string
	_ = filepath.WalkDir(outDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			matches = append(matches, path)
		}
		return nil
	})
	if len(matches) != 0 {
		t.Errorf("expected no files, got: %v", matches)
	}
}

func TestRunFiltersByCRDName(t *testing.T) {
	outDir := t.TempDir()
	crd1 := makeCRD("example.com", "Widget", "v1", true, true)
	crd2 := makeCRD("example.com", "Gadget", "v1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd1, &crd2)
	// CRDName "Widget" is not a builtin request, so builtinFetcher is never called.
	cfg := config.FetchConfig{OutputDir: outDir, CRDName: "Widget"}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), errorBuiltinMock()); err != nil {
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

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), validBuiltinMock()); err != nil {
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

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), validBuiltinMock()); err != nil {
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

func TestFetchAllIncludesBuiltinCRDSchema(t *testing.T) {
	outDir := t.TempDir()
	crd := makeCRD("example.com", "Widget", "v1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, All: true}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), validBuiltinMock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// User CRD schema is still written.
	if _, err := os.Stat(filepath.Join(outDir, "example.com", "widget_v1.json")); err != nil {
		t.Errorf("expected widget schema: %v", err)
	}

	// Built-in CRD schema is written at the expected path.
	builtinPath := filepath.Join(outDir, "apiextensions.k8s.io", "customresourcedefinition_v1.json")
	data, err := os.ReadFile(builtinPath)
	if err != nil {
		t.Fatalf("expected built-in CRD schema at %s: %v", builtinPath, err)
	}

	// All OpenAPI $ref paths must be rewritten to JSON Schema definitions paths.
	if bytes.Contains(data, []byte(`"#/components/schemas/`)) {
		t.Error("built-in schema still contains OpenAPI $ref paths")
	}
	if !bytes.Contains(data, []byte(`"#/definitions/`)) {
		t.Error("expected built-in schema refs to use #/definitions/")
	}

	// Root should redirect via $ref, not embed the CRD schema directly.
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("built-in schema is not valid JSON: %v", err)
	}
	ref, ok := schema["$ref"].(string)
	if !ok || !strings.HasSuffix(ref, ".CustomResourceDefinition") {
		t.Errorf("expected root $ref pointing at CustomResourceDefinition, got %v", schema["$ref"])
	}
	defs, ok := schema["definitions"].(map[string]any)
	if !ok {
		t.Fatal("expected definitions map in built-in schema")
	}
	if len(defs) != 3 {
		t.Errorf("expected 3 definitions (CRD, Spec, List), got %d", len(defs))
	}
}

func TestFetchCRDNameCustomResourceDefinitionsOnly(t *testing.T) {
	outDir := t.TempDir()
	// Provide a Widget CRD in the cluster; it must NOT be fetched.
	crd := makeCRD("example.com", "Widget", "v1", true, true)
	cs := fakeclient.NewSimpleClientset(&crd)
	cfg := config.FetchConfig{OutputDir: outDir, CRDName: "CustomResourceDefinitions"}

	if err := fetcher.RunWithClient(context.Background(), cfg, cs.ApiextensionsV1().CustomResourceDefinitions(), validBuiltinMock()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Built-in schema is written.
	builtinPath := filepath.Join(outDir, "apiextensions.k8s.io", "customresourcedefinition_v1.json")
	if _, err := os.Stat(builtinPath); err != nil {
		t.Errorf("expected built-in CRD schema: %v", err)
	}

	// User CRD schemas are NOT written.
	if _, err := os.Stat(filepath.Join(outDir, "example.com", "widget_v1.json")); err == nil {
		t.Error("widget schema should not be written when fetching only CustomResourceDefinitions")
	}
}
