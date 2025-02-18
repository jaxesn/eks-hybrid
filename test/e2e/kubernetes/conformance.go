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
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/hydrophone/pkg/common"
	"sigs.k8s.io/hydrophone/pkg/conformance"
	"sigs.k8s.io/hydrophone/pkg/conformance/client"
	"sigs.k8s.io/hydrophone/pkg/types"
)

type ConformanceTest struct {
	clientConfig      *rest.Config
	conformanceConfig types.Configuration
	conformanceClient *client.Client
	conformanceRunner *conformance.TestRunner
	k8s               *clientgo.Clientset
	logger            logr.Logger
}

func NewConformanceTest(clientConfig *rest.Config, k8s *clientgo.Clientset, logger logr.Logger) ConformanceTest {
	config := types.NewDefaultConfiguration()
	config.Parallel = 64

	conformanceImage, _ := getConformanceImage(k8s)
	config.ConformanceImage = conformanceImage

	testRunner := conformance.NewTestRunner(config, k8s)
	testClient := client.NewClient(clientConfig, k8s, config.Namespace)

	return ConformanceTest{
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
		return err
	}

	if err := c.conformanceRunner.Cleanup(ctx); err != nil {
		return err
	}
	focus := `\[NodeConformance\]`
	// focus := `Kubectl client Guestbook application should create and stop a working application`
	if err := c.conformanceRunner.Deploy(ctx, focus, true, 5*time.Minute); err != nil {
		return err
	}

	before := time.Now()

	spinner := common.NewSpinner(os.Stdout)
	spinner.Start()

	// PrintE2ELogs is a long running method
	if err := c.conformanceClient.PrintE2ELogs(ctx); err != nil {
		return err
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
