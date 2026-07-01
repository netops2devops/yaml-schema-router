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
