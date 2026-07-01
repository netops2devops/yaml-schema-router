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
