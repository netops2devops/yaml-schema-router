// Package kubernetes implements a schema detector for standard Kubernetes manifests.
package kubernetes

import (
	"fmt"
	"log"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/netops2devops/yaml-schema-router/internal/detector"
	"github.com/netops2devops/yaml-schema-router/internal/schemaregistry"
)

// K8sDetector implements the detector.Detector interface for Kubernetes manifests.
type K8sDetector struct {
	Registry          *schemaregistry.Registry
	SchemaRegistryURL string
	SchemaVersion     string
	SchemaFlavour     string
}

var _ detector.Detector = (*K8sDetector)(nil)

// K8sDetectorName is the unique identifier for the built-in Kubernetes detector.
const K8sDetectorName = "kubernetes-builtin"

// Name returns the unique string identifier for the Kubernetes detector.
func (d *K8sDetector) Name() string {
	return K8sDetectorName
}

type typeMeta struct {
	APIVersion string
	Kind       string
}

// Detect inspects the YAML content for all Kubernetes apiVersion and kind pairs
// to construct the appropriate schema URLs.
func (d *K8sDetector) Detect(_ string, content []byte) ([]string, error) {
	metas := extractAllTypeMeta(content)
	if len(metas) == 0 {
		return nil, nil
	}

	var schemaURLs []string

	for _, meta := range metas {
		if schemaURL := d.resolveSchemaURL(meta); schemaURL != "" {
			schemaURLs = append(schemaURLs, schemaURL)
		}
	}

	return schemaURLs, nil
}

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

	// Any group with a domain (e.g. "networking.k8s.io" or "gateway.networking.k8s.io")
	// is handled by CRDDetector instead: only it knows how to fall back from the
	// built-in registry to a local store or the CRD catalog. A dot-suffixed "k8s.io"
	// group is not necessarily part of core Kubernetes — SIG extension APIs like
	// Gateway API follow the same naming convention without shipping in kube-apiserver.
	if strings.Contains(group, ".") {
		log.Printf("[%s] Ignoring namespaced API group (group: %s); handled by %s", d.Name(), group, CRDDetectorName)
		return ""
	}

	return resolveBuiltinSchemaURL(d.Registry, d.SchemaRegistryURL, d.SchemaVersion, d.SchemaFlavour, d.Name(), group, version, meta.Kind)
}

// resolveBuiltinSchemaURL builds and fetches a schema URL from the built-in Kubernetes
// schema registry (e.g. yannh/kubernetes-json-schema) for the given group/version/kind.
// Shared between K8sDetector (core groups) and CRDDetector (k8s.io-suffixed groups that
// turn out to be true core types, e.g. "rbac.authorization.k8s.io"). Returns "" if the
// schema can't be resolved.
func resolveBuiltinSchemaURL(registry *schemaregistry.Registry, registryURL, schemaVersion, schemaFlavour, callerName, group, version, kind string) string {
	// Standardize the API group name for the schema registry by stripping the domain
	// e.g., "rbac.authorization.k8s.io" -> "rbac", "networking.k8s.io" -> "networking"
	formattedGroup := group
	if strings.Contains(formattedGroup, ".") && strings.HasSuffix(formattedGroup, "k8s.io") {
		formattedGroup = strings.Split(formattedGroup, ".")[0]
	}

	var apiVersionFormatted string
	if version != "" {
		apiVersionFormatted = fmt.Sprintf("%s-%s", formattedGroup, version)
	} else {
		apiVersionFormatted = formattedGroup // For core groups like "v1"
	}

	kindFormatted := strings.ToLower(kind)
	fileName := fmt.Sprintf("%s-%s.json", kindFormatted, apiVersionFormatted)
	versionDir := fmt.Sprintf("%s%s", schemaVersion, schemaFlavour)

	remoteSchemaURL, err := url.JoinPath(registryURL, versionDir, fileName)
	if err != nil {
		log.Printf("[%s] Failed to build built-in schema URL for %s: %v", callerName, kind, err)
		return ""
	}

	// Cache path is always rooted at K8sDetectorName so both detectors share one cache entry.
	cachePath := filepath.Join(K8sDetectorName, versionDir, fileName)
	localURI, err := registry.GetSchemaURI(remoteSchemaURL, cachePath)
	if err != nil {
		log.Printf("[%s] Failed to fetch built-in schema for %s: %v", callerName, kind, err)
		return ""
	}

	return localURI
}

// extractAllTypeMeta splits the raw YAML content by document separators
// and extracts the apiVersion and kind for each segment.
func extractAllTypeMeta(content []byte) []typeMeta {
	var metas []typeMeta
	docs := strings.SplitSeq(string(content), "---")

	for doc := range docs {
		var apiVersion, kind string
		for line := range strings.SplitSeq(doc, "\n") {
			// Only check top-level keys
			if after, ok := strings.CutPrefix(line, "apiVersion:"); ok {
				apiVersion = strings.TrimSpace(after)
				apiVersion = strings.Trim(apiVersion, `"'`)
			} else if after0, ok0 := strings.CutPrefix(line, "kind:"); ok0 {
				kind = strings.TrimSpace(after0)
				kind = strings.Trim(kind, `"'`)
			}

			if apiVersion != "" && kind != "" {
				break
			}
		}

		if apiVersion != "" && kind != "" {
			metas = append(metas, typeMeta{APIVersion: apiVersion, Kind: kind})
		}
	}

	return metas
}
