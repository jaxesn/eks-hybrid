package cleanup

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/integrii/flaggy"
	"go.uber.org/zap"
	"gopkg.in/yaml.v2"

	"github.com/aws/eks-hybrid/internal/cli"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/cleanup"
	"github.com/aws/eks-hybrid/test/e2e/cluster"
)

type Command struct {
	flaggy            *flaggy.Subcommand
	resourcesFilePath string
	clusterPrefix     string
	ageThreshold      time.Duration
	dryRun            bool
	all               bool
}

func NewCommand() *Command {
	cmd := Command{
		ageThreshold: 24 * time.Hour,
	}

	cleanup := flaggy.NewSubcommand("cleanup")
	cleanup.Description = "Delete the E2E test infrastructure"
	cleanup.AdditionalHelpPrepend = "This command will cleanup E2E test infrastructure."

	cleanup.String(&cmd.resourcesFilePath, "f", "filename", "Path to resources file")
	cleanup.String(&cmd.clusterPrefix, "p", "cluster-prefix", "Cluster name prefix to cleanup (will append * for search)")
	cleanup.Duration(&cmd.ageThreshold, "a", "age-threshold", "Age threshold for instance deletion")
	cleanup.Bool(&cmd.dryRun, "dry-run", "dry-run", "Simulate the cleanup without making any changes")
	cleanup.Bool(&cmd.all, "all", "all", "Include all resources based on the age threshold in the cleanup")

	cmd.flaggy = cleanup

	return &cmd
}

func (c *Command) Flaggy() *flaggy.Subcommand {
	return c.flaggy
}

func (c *Command) Commands() []cli.Command {
	return []cli.Command{c}
}

func (s *Command) Run(log *zap.Logger, opts *cli.GlobalOptions) error {
	ctx := context.Background()
	logger := e2e.NewLogger()

	if s.clusterPrefix == "" && !s.all && s.resourcesFilePath == "" {
		return fmt.Errorf("either --cluster-prefix or --all or --filename must be specified")
	}

	var deleteCluster cluster.DeleteInput
	if s.resourcesFilePath != "" {
		file, err := os.ReadFile(s.resourcesFilePath)
		if err != nil {
			return fmt.Errorf("failed to open configuration file: %w", err)
		}

		if err = yaml.Unmarshal(file, &deleteCluster); err != nil {
			return fmt.Errorf("unmarshaling cleanup config: %w", err)
		}
	}

	var loadOpts []func(*config.LoadOptions) error
	if deleteCluster.ClusterRegion != "" {
		loadOpts = append(loadOpts, config.WithRegion(deleteCluster.ClusterRegion))
	}

	aws, err := config.LoadDefaultConfig(ctx, loadOpts...)
	if err != nil {
		return fmt.Errorf("reading AWS configuration: %w", err)
	}

	if deleteCluster.ClusterRegion != "" {
		delete := cluster.NewDelete(aws, logger, deleteCluster.Endpoint)
		logger.Info("Cleaning up E2E cluster resources...", "cluster", deleteCluster.ClusterName)
		if err = delete.Run(ctx, deleteCluster); err != nil {
			return fmt.Errorf("error cleaning up e2e resources: %w", err)
		}
		logger.Info("Cleanup completed successfully!")
		return nil
	}

	sweeper := cleanup.NewSweeper(aws, logger)
	input := cleanup.SweeperInput{
		AllClusters:          s.all,
		ClusterNamePrefix:    s.clusterPrefix,
		InstanceAgeThreshold: s.ageThreshold,
		DryRun:               s.dryRun,
	}
	logger.Info("Cleaning up E2E cluster resources...", "configuration", input)
	if err = sweeper.Run(ctx, input); err != nil {
		return fmt.Errorf("error cleaning up e2e resources: %w", err)
	}
	logger.Info("Cleanup completed successfully!")
	return nil
}
