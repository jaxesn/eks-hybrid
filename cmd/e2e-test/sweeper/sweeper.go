package sweeper

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/ratelimit"
	"github.com/aws/aws-sdk-go-v2/aws/retry"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/integrii/flaggy"
	"go.uber.org/zap"

	"github.com/aws/eks-hybrid/internal/cli"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/cleanup"
)

type SweeperCommand struct {
	flaggy        *flaggy.Subcommand
	clusterPrefix string
	clusterName   string
	ageThreshold  time.Duration
	dryRun        bool
	all           bool
}

func NewSweeperCommand() *SweeperCommand {
	cmd := SweeperCommand{
		ageThreshold: 24 * time.Hour,
	}

	sweeper := flaggy.NewSubcommand("sweeper")
	sweeper.Description = "Sweep and delete E2E test infrastructure based on criteria"
	sweeper.AdditionalHelpPrepend = "This command will sweep and cleanup E2E test infrastructure based on specified criteria."

	sweeper.String(&cmd.clusterPrefix, "p", "cluster-prefix", "Cluster name prefix to cleanup (will append * for search)")
	sweeper.String(&cmd.clusterName, "c", "cluster-name", "Specific cluster name to cleanup")
	sweeper.Duration(&cmd.ageThreshold, "", "age", "Age threshold for instance deletion")
	sweeper.Bool(&cmd.dryRun, "", "dry-run", "Simulate the cleanup without making any changes")
	sweeper.Bool(&cmd.all, "", "all", "Include all resources based on the age threshold in the cleanup")

	cmd.flaggy = sweeper

	return &cmd
}

func (c *SweeperCommand) Flaggy() *flaggy.Subcommand {
	return c.flaggy
}

func (c *SweeperCommand) Commands() []cli.Command {
	return []cli.Command{c}
}

func (s *SweeperCommand) Run(log *zap.Logger, opts *cli.GlobalOptions) error {
	ctx := context.Background()
	logger := e2e.NewLogger()

	if s.clusterPrefix != "" && s.clusterName != "" {
		return fmt.Errorf("cannot use --cluster-prefix and --cluster-name together")
	}

	if s.clusterPrefix != "" && s.all {
		return fmt.Errorf("cannot use --cluster-prefix and --all together")
	}

	if s.clusterName != "" && s.all {
		return fmt.Errorf("cannot use --cluster-name and --all together")
	}

	if s.clusterPrefix == "" && s.clusterName == "" && !s.all {
		return fmt.Errorf("either --cluster-prefix, --cluster-name, or --all must be specified")
	}
	aws, err := config.LoadDefaultConfig(ctx, config.WithRetryer(func() aws.Retryer {
		return retry.NewAdaptiveMode(func(o *retry.AdaptiveModeOptions) {
			// the adaptive retryer wraps the standard retryer but implements a custom rate limiterfor getting the AttemptToken
			// which the sdk calls internally before making a request (including retried requests)
			// when getting this GetAttemptToken it will sleep if neccessary based on its internal rate limiter
			// However, when a request fails, the sdk calls GetRetryToken, which adapative sends its wrapped standard retryer
			// the standard retryer uses the TokenRateLimit to make a determination of whether to retry or not and its pretty tight
			// this disables the TokenRateLimit on the standard retryer by setting it to the None implementation
			// see for more:
			//	https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-retries-timeouts.html
			//	https://github.com/aws/aws-sdk-go-v2/blob/main/aws/retry/adaptive.go
			//	https://github.com/aws/aws-sdk-go-v2/blob/main/aws/retry/standard.go
			o.StandardOptions = []func(*retry.StandardOptions){
				func(o *retry.StandardOptions) {
					o.MaxAttempts = 40
					o.RateLimiter = ratelimit.None
				},
			}
		})
	}))
	if err != nil {
		return fmt.Errorf("reading AWS configuration: %w", err)
	}

	sweeper := cleanup.NewSweeper(aws, logger)
	input := cleanup.SweeperInput{
		AllClusters:          s.all,
		ClusterNamePrefix:    s.clusterPrefix,
		ClusterName:          s.clusterName,
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
