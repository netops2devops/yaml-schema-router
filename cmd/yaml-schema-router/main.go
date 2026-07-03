// Package main is the entry point for yaml-schema-router.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
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
			viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
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
				LogFile:           expandTilde(viper.GetString("log-file")),
				LspPath:           viper.GetString("lsp-path"),
				CRDSchemaDir:      expandTilde(viper.GetString("crd-schema-dir")),
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
				Kubeconfig: expandTilde(viper.GetString("kubeconfig")),
				OutputDir:  expandTilde(viper.GetString("output-dir")),
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

	var registry *schemaregistry.Registry
	var err error
	if cfg.CRDSchemaDir != "" {
		registry, err = schemaregistry.NewRegistryAt(cfg.CRDSchemaDir)
	} else {
		registry, err = schemaregistry.NewRegistry()
	}
	if err != nil {
		return fmt.Errorf("schema registry: %w", err)
	}
	registry.SetTimeout(cfg.DownloadTimeout)

	k8sDetector := &kubernetes.K8sDetector{
		Registry:          registry,
		SchemaRegistryURL: cfg.K8sSchemaRegistry,
		SchemaVersion:     cfg.K8sSchemaVersion,
		SchemaFlavour:     cfg.K8sSchemaFlavour,
	}
	crdDetector := &kubernetes.CRDDetector{
		Registry:              registry,
		CRDSchemaRegistryURL:  cfg.CRDSchemaRegistry,
		K8sSchemaRegistryURL:  cfg.K8sSchemaRegistry,
		K8sSchemaVersion:      cfg.K8sSchemaVersion,
		K8sSchemaFlavour:      cfg.K8sSchemaFlavour,
		K8sMetaSchemaFileName: config.DefaultK8sMetaSchemaFileName,
		LocalSchemaDir:        cfg.CRDSchemaDir,
		FallbackRemote:        cfg.CRDFallbackRemote,
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
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return os.TempDir()
	}
	return filepath.Join(homeDir, ".config")
}

// expandTilde replaces a leading "~/" with the user's home directory.
// Viper does not expand tildes, so paths from config files need this treatment.
func expandTilde(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return filepath.Join(homeDir, path[2:])
}
