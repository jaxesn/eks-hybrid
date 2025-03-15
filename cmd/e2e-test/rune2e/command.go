package rune2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/go-logr/logr"
	"github.com/integrii/flaggy"
	"go.uber.org/zap"
	"gopkg.in/yaml.v2"

	"github.com/aws/eks-hybrid/internal/cli"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/cluster"
	"github.com/aws/eks-hybrid/test/e2e/run"
	"github.com/aws/eks-hybrid/test/e2e/suite"
)

const (
	defaultNodeadmAMDURL     = "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/amd64/nodeadm"
	defaultNodeadmARMURL     = "https://hybrid-assets.eks.amazonaws.com/releases/latest/bin/linux/arm64/nodeadm"
	defaultClusterNamePrefix = "nodeadm-e2e-tests"
	defaultRegion            = "us-west-2"
	defaultK8sVersion        = "1.31"
	defaultCNI               = "cilium"
	defaultTimeout           = "60m"
	defaultTestProcs         = 1
	defaultTestsBinaryOrPath = "./test/e2e/suite"
)

type command struct {
	artifactsDir      string
	clusterName       string
	cni               string
	endpoint          string
	k8sVersion        string
	logsBucket        string
	noColor           bool
	nodeadmAMDURL     string
	nodeadmARMURL     string
	region            string
	setupConfigFile   string
	skipCleanup       bool
	skippedTests      string
	subCmd            *flaggy.Subcommand
	testConfigFile    string
	testLabelFilter   string
	testProcs         int
	testsBinaryOrPath string
	testTimeout       string
}

func NewCommand() *command {
	cmd := &command{
		clusterName:       fmt.Sprintf("%s-%s", defaultClusterNamePrefix, strings.ReplaceAll(defaultK8sVersion, ".", "-")),
		cni:               defaultCNI,
		k8sVersion:        defaultK8sVersion,
		nodeadmAMDURL:     defaultNodeadmAMDURL,
		nodeadmARMURL:     defaultNodeadmARMURL,
		skipCleanup:       false,
		region:            defaultRegion,
		subCmd:            flaggy.NewSubcommand("run-e2e"),
		testProcs:         defaultTestProcs,
		testTimeout:       defaultTimeout,
		testsBinaryOrPath: defaultTestsBinaryOrPath,
	}
	cmd.subCmd.Description = "Run E2E tests"
	cmd.subCmd.String(&cmd.clusterName, "n", "name", "Cluster name (optional)")
	cmd.subCmd.String(&cmd.region, "r", "region", "AWS region (optional)")
	cmd.subCmd.String(&cmd.k8sVersion, "k", "kubernetes-version", "Kubernetes version (optional)")
	cmd.subCmd.String(&cmd.cni, "c", "cni", "CNI plugin (optional)")
	cmd.subCmd.String(&cmd.nodeadmAMDURL, "", "nodeadm-amd-url", "NodeADM AMD URL (optional)")
	cmd.subCmd.String(&cmd.nodeadmARMURL, "", "nodeadm-arm-url", "NodeADM ARM URL (optional)")
	cmd.subCmd.String(&cmd.logsBucket, "b", "logs-bucket", "S3 bucket for logs (optional)")
	cmd.subCmd.String(&cmd.artifactsDir, "a", "artifacts-dir", "Directory for artifacts (optional, defaults to a new temp directory)")
	cmd.subCmd.String(&cmd.endpoint, "e", "endpoint", "AWS endpoint (optional)")
	cmd.subCmd.String(&cmd.skippedTests, "s", "skipped-tests", "ginkgo regex to skip tests (optional)")
	cmd.subCmd.String(&cmd.testTimeout, "", "timeout", "Timeout for the test (optional)")
	cmd.subCmd.String(&cmd.testLabelFilter, "f", "test-filter", "Filter for the test (optional)")
	cmd.subCmd.Bool(&cmd.skipCleanup, "", "skip-cleanup", "Skip cleanup (optional)")
	cmd.subCmd.String(&cmd.testsBinaryOrPath, "", "tests-binary", "Path to the tests binary (optional)")
	cmd.subCmd.Bool(&cmd.noColor, "", "no-color", "Disable color output (optional)")
	cmd.subCmd.Int(&cmd.testProcs, "p", "procs", "Number of processes to run (optional)")
	cmd.subCmd.String(&cmd.setupConfigFile, "", "setup-config", "Path to a YAML file containing cluster.TestResources configuration (optional)")
	cmd.subCmd.String(&cmd.testConfigFile, "", "test-config", "Path to a YAML file containing suite.TestConfig configuration (optional)")
	return cmd
}

func (c *command) Flaggy() *flaggy.Subcommand {
	return c.subCmd
}

func (c *command) Commands() []cli.Command {
	return []cli.Command{c}
}

func (c *command) Run(log *zap.Logger, opts *cli.GlobalOptions) error {
	ctx := context.Background()
	logger := e2e.NewLogger(e2e.LoggerConfig{NoColor: c.noColor})

	awsCfg, err := config.LoadDefaultConfig(ctx, config.WithRegion(c.region))
	if err != nil {
		return fmt.Errorf("reading AWS configuration: %w", err)
	}

	testResources, err := c.loadSetupConfig(logger)
	if err != nil {
		return fmt.Errorf("loading test resources configuration: %w", err)
	}

	testConfig, err := c.loadTestConfig(testResources, logger)
	if err != nil {
		return fmt.Errorf("loading test configuration: %w", err)
	}

	artifactsDir, err := c.getArtifactsDir(c.artifactsDir, logger)
	if err != nil {
		return fmt.Errorf("getting artifacts directory: %w", err)
	}

	testConfig.ArtifactsFolder = artifactsDir

	// Run E2E tests
	e2e := run.E2E{
		AwsCfg:            awsCfg,
		Logger:            logger,
		NoColor:           c.noColor,
		TestConfig:        testConfig,
		TestLabelFilter:   c.testLabelFilter,
		TestProcs:         c.testProcs,
		TestTimeout:       c.testTimeout,
		TestsBinaryOrPath: c.testsBinaryOrPath,
		TestResources:     testResources,
		SkipCleanup:       c.skipCleanup,
		SkippedTests:      c.skippedTests,
	}
	if err := e2e.Run(ctx); err != nil {
		return fmt.Errorf("running E2E tests: %w", err)
	}

	return nil
}

func (c *command) getArtifactsDir(artifactsDir string, logger logr.Logger) (string, error) {
	var err error
	if artifactsDir == "" {
		artifactsDir, err = os.MkdirTemp("", "eks-hybrid-e2e-*")
		if err != nil {
			return "", fmt.Errorf("creating temp directory: %w", err)
		}
		logger.Info("Created temporary test directory", "path", artifactsDir)
	}
	artifactsDir, err = filepath.Abs(artifactsDir)
	if err != nil {
		return "", fmt.Errorf("getting absolute path for artifacts: %w", err)
	}
	return artifactsDir, nil
}

// loadSetupConfig loads the TestResources configuration from a file.
// It validates that no individual resource flags are set when using a config file.
func (c *command) loadSetupConfig(logger logr.Logger) (cluster.TestResources, error) {
	// Initialize default test resources
	testResources := cluster.TestResources{
		ClusterName:       c.clusterName,
		ClusterRegion:     c.region,
		KubernetesVersion: c.k8sVersion,
		Cni:               c.cni,
		Endpoint:          c.endpoint,
		ClusterNetwork: cluster.NetworkConfig{
			VpcCidr:           "10.0.0.0/16",
			PublicSubnetCidr:  "10.0.10.0/24",
			PrivateSubnetCidr: "10.0.20.0/24",
		},
		HybridNetwork: cluster.NetworkConfig{
			VpcCidr:           "10.1.0.0/16",
			PublicSubnetCidr:  "10.1.1.0/24",
			PrivateSubnetCidr: "10.1.2.0/24",
			PodCidr:           "10.2.0.0/16",
		},
	}

	if c.setupConfigFile != "" {
		// Validate that individual resource flags are not also set
		defaultClusterName := fmt.Sprintf("%s-%s", defaultClusterNamePrefix, strings.ReplaceAll(defaultK8sVersion, ".", "-"))
		if c.clusterName != defaultClusterName ||
			c.region != defaultRegion ||
			c.k8sVersion != defaultK8sVersion ||
			c.cni != defaultCNI ||
			c.endpoint != "" {
			return testResources, fmt.Errorf("cannot specify both setup-config file and individual cluster resource flags (name, region, kubernetes-version, cni, endpoint)")
		}

		// Load test resources from file
		setupConfigData, err := os.ReadFile(c.setupConfigFile)
		if err != nil {
			return testResources, fmt.Errorf("reading setup config file: %w", err)
		}

		if err := yaml.Unmarshal(setupConfigData, &testResources); err != nil {
			return testResources, fmt.Errorf("unmarshaling setup config: %w", err)
		}

		logger.Info("Loaded test resources configuration from file", "path", c.setupConfigFile)
	}

	return testResources, nil
}

// loadTestConfig loads the TestConfig configuration from a file.
// It validates that no individual test config flags are set when using a config file.
func (c *command) loadTestConfig(testResources cluster.TestResources, logger logr.Logger) (suite.TestConfig, error) {
	testConfig := suite.TestConfig{
		ClusterName:   testResources.ClusterName,
		ClusterRegion: testResources.ClusterRegion,
		Endpoint:      testResources.Endpoint,
		NodeadmUrlAMD: c.nodeadmAMDURL,
		NodeadmUrlARM: c.nodeadmARMURL,
		LogsBucket:    c.logsBucket,
	}

	if c.testConfigFile != "" {
		// Validate that individual test config flags are not also set
		if c.nodeadmAMDURL != defaultNodeadmAMDURL ||
			c.nodeadmARMURL != defaultNodeadmARMURL ||
			c.logsBucket != "" ||
			c.artifactsDir != "" {
			return testConfig, fmt.Errorf("cannot specify both test-config file and individual test config flags (nodeadm-amd-url, nodeadm-arm-url, logs-bucket, artifacts-dir)")
		}

		// Load test config from file
		testConfigData, err := os.ReadFile(c.testConfigFile)
		if err != nil {
			return testConfig, fmt.Errorf("reading test config file: %w", err)
		}

		if err := yaml.Unmarshal(testConfigData, &testConfig); err != nil {
			return testConfig, fmt.Errorf("unmarshaling test config: %w", err)
		}

		logger.Info("Loaded test configuration from file", "path", c.testConfigFile)
	}

	return testConfig, nil
}
