package kubernetes

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/blang/semver/v4"
	"github.com/go-logr/logr"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/hydrophone/pkg/common"
	"sigs.k8s.io/hydrophone/pkg/conformance"
	"sigs.k8s.io/hydrophone/pkg/conformance/client"
	"sigs.k8s.io/hydrophone/pkg/types"
)

type ConformanceTest struct {
	Namespace         string
	clientConfig      *rest.Config
	conformanceConfig types.Configuration
	conformanceClient *client.Client
	conformanceRunner *conformance.TestRunner
	k8s               *kubernetes.Clientset
	logger            logr.Logger
}

type ConformanceOption func(*types.Configuration)

func WithOutputDir(outputDir string) ConformanceOption {
	return func(c *types.Configuration) {
		c.OutputDir = outputDir
	}
}

func NewConformanceTest(clientConfig *rest.Config, k8s *kubernetes.Clientset, logger logr.Logger, opts ...ConformanceOption) ConformanceTest {
	config := types.NewDefaultConfiguration()
	config.Parallel = 64

	for _, opt := range opts {
		opt(&config)
	}

	conformanceImage, _ := getConformanceImage(k8s)
	config.ConformanceImage = conformanceImage

	testRunner := conformance.NewTestRunner(config, k8s)
	testClient := client.NewClient(clientConfig, k8s, config.Namespace)

	return ConformanceTest{
		Namespace:         config.Namespace,
		clientConfig:      clientConfig,
		conformanceConfig: config,
		conformanceClient: testClient,
		conformanceRunner: testRunner,
		k8s:               k8s,
		logger:            logger,
	}
}

func (c ConformanceTest) Cleanup(ctx context.Context) error {
	return c.conformanceRunner.Cleanup(ctx)
}

func (c ConformanceTest) CollectLogs(ctx context.Context) error {
	return c.conformanceClient.FetchFiles(ctx, c.conformanceConfig.OutputDir)
}

func (c ConformanceTest) FetchExitCode(ctx context.Context) (int, error) {
	return c.conformanceClient.FetchExitCode(ctx)
}

func (c ConformanceTest) Run(ctx context.Context) error {
	if err := c.conformanceRunner.PrintListImages(ctx, 5*time.Minute); err != nil {
		return fmt.Errorf("printing images: %w", err)
	}

	if err := c.conformanceRunner.Cleanup(ctx); err != nil {
		return fmt.Errorf("cleaning up: %w", err)
	}

	if err := WaitForNamespaceToBeDeleted(ctx, c.k8s, c.conformanceConfig.Namespace); err != nil {
		return fmt.Errorf("waiting for namespace to be deleted: %w", err)
	}

	// focus := `\[NodeConformance\]`
	focus := `Projected configMap should be consumable in multiple volumes in the same pod`
	if err := c.conformanceRunner.Deploy(ctx, focus, "", true, 5*time.Minute); err != nil {
		return fmt.Errorf("deploying: %w", err)
	}

	before := time.Now()

	spinner := common.NewSpinner(os.Stdout)
	spinner.Start()

	// PrintE2ELogs is a long running method
	if err := c.conformanceClient.PrintE2ELogs(ctx); err != nil {
		return fmt.Errorf("printing logs: %w", err)
	}
	spinner.Stop()

	c.logger.Info(fmt.Sprintf("Tests finished after %v.", time.Since(before).Round(time.Second)))

	return nil
}

func getConformanceImage(clientset *kubernetes.Clientset) (string, error) {
	serverVersion, err := clientset.ServerVersion()
	if err != nil {
		return "", fmt.Errorf("failed fetching server version: %w", err)
	}

	normalized, err := normalizeVersion(serverVersion.String())
	if err != nil {
		return "", fmt.Errorf("failed parsing server version: %w", err)
	}

	conformanceImage := fmt.Sprintf("registry.k8s.io/conformance:%s", normalized)

	return conformanceImage, nil
}

func normalizeVersion(ver string) (string, error) {
	ver = strings.TrimPrefix(ver, "v")

	parsedVersion, err := semver.Parse(ver)
	if err != nil {
		return "", fmt.Errorf("error parsing conformance image tag: %w", err)
	}

	return "v" + parsedVersion.FinalizeVersion(), nil
}
