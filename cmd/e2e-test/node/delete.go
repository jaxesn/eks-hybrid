package node

import (
	"context"
	"fmt"

	sdk "github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	ec2sdk "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	s3sdk "github.com/aws/aws-sdk-go-v2/service/s3"
	ssmsdk "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/integrii/flaggy"
	"go.uber.org/zap"
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aws/eks-hybrid/internal/cli"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/cluster"
	"github.com/aws/eks-hybrid/test/e2e/ec2"
	"github.com/aws/eks-hybrid/test/e2e/peered"
	"github.com/aws/eks-hybrid/test/e2e/ssm"
)

type Delete struct {
	flaggy       *flaggy.Subcommand
	configFile   string
	instanceName string
}

func NewDeleteCommand() *Delete {
	cmd := &Delete{}

	deleteCmd := flaggy.NewSubcommand("delete")
	deleteCmd.Description = "Delete a Hybrid Node"
	deleteCmd.AddPositionalValue(&cmd.instanceName, "INSTANCE_NAME", 1, true, "Name of the instance to delete.")
	deleteCmd.String(&cmd.configFile, "f", "config-file", "Path tests config file.")

	cmd.flaggy = deleteCmd

	return cmd
}

func (d *Delete) Flaggy() *flaggy.Subcommand {
	return d.flaggy
}

func (d *Delete) Run(log *zap.Logger, opts *cli.GlobalOptions) error {
	ctx := context.Background()
	config, err := e2e.ReadConfig(d.configFile)
	if err != nil {
		return err
	}

	logger := e2e.NewLogger()
	aws, err := e2e.NewAWSConfig(ctx, awsconfig.WithRegion(config.ClusterRegion))
	if err != nil {
		return fmt.Errorf("reading AWS configuration: %w", err)
	}

	ec2Client := ec2sdk.NewFromConfig(aws)
	ssmClient := ssmsdk.NewFromConfig(aws)
	s3Client := s3sdk.NewFromConfig(aws)

	instances, err := ec2Client.DescribeInstances(ctx, &ec2sdk.DescribeInstancesInput{
		Filters: []types.Filter{
			{
				Name:   sdk.String("tag:Name"),
				Values: []string{d.instanceName},
			},
			{
				Name:   sdk.String("instance-state-name"),
				Values: []string{"running", "pending"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing instance %s: %w", d.instanceName, err)
	}
	if len(instances.Reservations) == 0 || len(instances.Reservations[0].Instances) == 0 {
		return fmt.Errorf("no instance found with name %s", d.instanceName)
	}

	instance := instances.Reservations[0].Instances[0]

	clientConfig, err := clientcmd.BuildConfigFromFlags("", cluster.KubeconfigPath(config.ClusterName))
	if err != nil {
		return err
	}
	k8s, err := clientgo.NewForConfig(clientConfig)
	if err != nil {
		return err
	}

	jumpbox, err := peered.JumpboxInstance(ctx, ec2Client, config.ClusterName)
	if err != nil {
		return err
	}

	commandRunner := ssm.NewSSHOnSSMCommandRunner(ssmClient, *jumpbox.InstanceId, logger)

	node := peered.NodeCleanup{
		EC2:                 ec2Client,
		S3:                  s3Client,
		K8s:                 k8s,
		RemoteCommandRunner: commandRunner,
		Logger:              logger,
		ClusterName:         config.ClusterName,
		LogsBucket:          config.LogsBucket,
	}

	if err := node.Cleanup(ctx, peered.PeeredNode{
		Instance: ec2.Instance{
			ID:   *instance.InstanceId,
			IP:   *instance.PrivateIpAddress,
			Name: d.instanceName,
		},
		Name: d.instanceName,
		OS:   nil,
	}); err != nil {
		return err
	}

	return nil
}
