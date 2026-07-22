package kubernetes

import (
	"encoding/json"
	"fmt"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/netops2devops/yaml-schema-router/internal/detector"
	"github.com/netops2devops/yaml-schema-router/internal/schemaregistry"
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
	Registry              *schemaregistry.Registry
	CRDSchemaRegistryURL  string
	K8sSchemaRegistryURL  string
	K8sSchemaVersion      string
	K8sSchemaFlavour      string
	K8sMetaSchemaFileName string
	LocalSchemaDir        string
	FallbackRemote        bool
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
		if !found || !strings.Contains(group, ".") {
			continue
		}

		// A "k8s.io"-suffixed group might be a true core Kubernetes type, or it might be
		// a SIG extension API (Gateway API, etc.) that only follows the naming convention.
		// Try, in order: schema already in the local CRD store, the built-in registry,
		// then fall through below to the CRD catalog like any other custom resource.
		if strings.HasSuffix(group, "k8s.io") {
			if uri := d.resolveDirectFromLocalStore(group, meta.Kind, version); uri != "" {
				schemaURLs = append(schemaURLs, uri)
				continue
			}
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

		if strings.HasSuffix(group, "k8s.io") {
			if uri := resolveBuiltinSchemaURL(d.Registry, d.K8sSchemaRegistryURL, d.K8sSchemaVersion, d.K8sSchemaFlavour, d.Name(), group, version, meta.Kind); uri != "" {
				schemaURLs = append(schemaURLs, uri)
				continue
			}
			log.Printf("[%s] No built-in schema for %s/%s; falling back to CRD catalog", d.Name(), group, meta.Kind)
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
	objectMetaURL, err := url.JoinPath(d.K8sSchemaRegistryURL, versionDir, d.K8sMetaSchemaFileName)
	if err != nil {
		return "", "", err
	}
	metaCachePath := filepath.Join(K8sDetectorName, versionDir, d.K8sMetaSchemaFileName)
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

// resolveDirectFromLocalStore serves a schema straight from the local store without
// generating an ObjectMeta wrapper. Used for built-in k8s types whose fetched schemas
// are already self-contained (they include all definitions from the cluster's OpenAPI v3 doc).
func (d *CRDDetector) resolveDirectFromLocalStore(group, kind, version string) string {
	if d.LocalSchemaDir == "" {
		return ""
	}
	kindLower := strings.ToLower(kind)
	fileName := fmt.Sprintf("%s_%s.json", kindLower, version)
	cachePath := filepath.Join(d.Name(), group, fileName)

	if _, err := os.Stat(d.Registry.GetLocalPath(cachePath)); err == nil {
		log.Printf("[%s] Cache hit for built-in %s/%s", d.Name(), group, fileName)
		return d.Registry.GetLocalFileURI(cachePath)
	}

	localPath := filepath.Join(d.LocalSchemaDir, group, fileName)
	data, err := os.ReadFile(localPath)
	if err != nil {
		log.Printf("[%s] Built-in schema not found at %s; run 'yaml-schema-router fetch %s'", d.Name(), localPath, kind)
		return ""
	}

	log.Printf("[%s] Local store hit for built-in: %s", d.Name(), localPath)
	if saveErr := d.Registry.SaveLocalSchema(cachePath, data); saveErr != nil {
		log.Printf("[%s] Failed to cache built-in schema: %v", d.Name(), saveErr)
		return ""
	}
	return d.Registry.GetLocalFileURI(cachePath)
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
