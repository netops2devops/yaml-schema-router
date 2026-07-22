package fetcher

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	apiextensionsclient "k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/netops2devops/yaml-schema-router/internal/config"
)

const (
	builtinCRDGroup = "apiextensions.k8s.io"
	builtinCRDKind  = "CustomResourceDefinition"
	builtinCRDVer   = "v1"
)

// CRDLister is satisfied by the real and fake k8s CRD client.
type CRDLister interface {
	List(ctx context.Context, opts metav1.ListOptions) (*apiextensionsv1.CustomResourceDefinitionList, error)
}

// BuiltinSchemaFetcher fetches the raw OpenAPI v3 document for a Kubernetes API group/version.
type BuiltinSchemaFetcher interface {
	FetchGroupVersionOpenAPI(ctx context.Context, group, version string) ([]byte, error)
}

type discoveryFetcher struct {
	client discovery.DiscoveryInterface
}

func (f *discoveryFetcher) FetchGroupVersionOpenAPI(_ context.Context, group, version string) ([]byte, error) {
	paths, err := f.client.OpenAPIV3().Paths()
	if err != nil {
		return nil, fmt.Errorf("OpenAPI v3 paths: %w", err)
	}
	path := fmt.Sprintf("apis/%s/%s", group, version)
	gv, ok := paths[path]
	if !ok {
		return nil, fmt.Errorf("OpenAPI path %q not found in cluster", path)
	}
	return gv.Schema("application/json")
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
	dc, err := discovery.NewDiscoveryClientForConfig(restCfg)
	if err != nil {
		return fmt.Errorf("discovery client: %w", err)
	}
	return RunWithClient(
		context.Background(),
		cfg,
		cs.ApiextensionsV1().CustomResourceDefinitions(),
		&discoveryFetcher{client: dc},
	)
}

// RunWithClient is the testable core: accepts injected CRDLister and BuiltinSchemaFetcher.
func RunWithClient(ctx context.Context, cfg config.FetchConfig, client CRDLister, builtinFetcher BuiltinSchemaFetcher) error {
	fetchBuiltin := cfg.All || (cfg.CRDName != "" && isBuiltinCRDRequest(cfg.CRDName))
	fetchUserCRDs := cfg.All || (cfg.CRDName != "" && !isBuiltinCRDRequest(cfg.CRDName))

	written := 0

	if fetchUserCRDs {
		list, err := client.List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("listing CRDs: %w", err)
		}
		items := list.Items
		if cfg.CRDName != "" && !cfg.All {
			items = filterByName(items, cfg.CRDName)
		}
		for _, crd := range items {
			if err := writeCRD(cfg.OutputDir, crd); err != nil {
				fmt.Fprintf(os.Stderr, "skipping %s: %v\n", crd.Name, err)
				continue
			}
			written++
		}
	}

	if fetchBuiltin {
		if err := writeBuiltinCRDSchema(ctx, cfg.OutputDir, builtinFetcher); err != nil {
			fmt.Fprintf(os.Stderr, "skipping built-in CRD schema: %v\n", err)
		} else {
			written++
		}
	}

	fmt.Printf("Done. %d schema(s) written to %s\n", written, cfg.OutputDir)
	return nil
}

// isBuiltinCRDRequest reports whether name refers to the built-in CustomResourceDefinition type.
func isBuiltinCRDRequest(name string) bool {
	lower := strings.ToLower(name)
	return lower == "customresourcedefinition" ||
		lower == "customresourcedefinitions" ||
		lower == "customresourcedefinitions.apiextensions.k8s.io"
}

func writeBuiltinCRDSchema(ctx context.Context, outputDir string, fetcher BuiltinSchemaFetcher) error {
	raw, err := fetcher.FetchGroupVersionOpenAPI(ctx, builtinCRDGroup, builtinCRDVer)
	if err != nil {
		return fmt.Errorf("fetch OpenAPI: %w", err)
	}

	schema, err := extractJSONSchema(raw, builtinCRDKind)
	if err != nil {
		return fmt.Errorf("extract schema: %w", err)
	}

	kindLower := strings.ToLower(builtinCRDKind)
	fileName := fmt.Sprintf("%s_%s.json", kindLower, builtinCRDVer)
	dir := filepath.Join(outputDir, builtinCRDGroup)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(dir, fileName)
	if err := os.WriteFile(path, schema, 0o644); err != nil {
		return err
	}

	fmt.Printf("Fetched: %s/%s\n", builtinCRDGroup, fileName)
	return nil
}

// extractJSONSchema converts a raw OpenAPI v3 document into a JSON Schema draft-07 document.
// It rewrites all "#/components/schemas/" refs to "#/definitions/" and places the schema
// for kindName at the root via "$ref".
func extractJSONSchema(rawOpenAPI []byte, kindName string) ([]byte, error) {
	rewritten := bytes.ReplaceAll(rawOpenAPI,
		[]byte(`"#/components/schemas/`),
		[]byte(`"#/definitions/`))

	var doc struct {
		Components struct {
			Schemas map[string]json.RawMessage `json:"schemas"`
		} `json:"components"`
	}
	if err := json.Unmarshal(rewritten, &doc); err != nil {
		return nil, fmt.Errorf("parse OpenAPI: %w", err)
	}
	if len(doc.Components.Schemas) == 0 {
		return nil, fmt.Errorf("no schemas found in OpenAPI document")
	}

	suffix := "." + kindName
	var crdKey string
	for k := range doc.Components.Schemas {
		if strings.HasSuffix(k, suffix) {
			crdKey = k
			break
		}
	}
	if crdKey == "" {
		return nil, fmt.Errorf("schema for %s not found in OpenAPI document", kindName)
	}

	result := map[string]any{
		"$schema":     "https://json-schema.org/draft-07/schema#",
		"$ref":        "#/definitions/" + crdKey,
		"definitions": doc.Components.Schemas,
	}
	return json.MarshalIndent(result, "", "  ")
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
		"$schema": "https://json-schema.org/draft-07/schema#",
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
