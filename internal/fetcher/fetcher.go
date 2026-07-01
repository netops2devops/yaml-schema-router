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
