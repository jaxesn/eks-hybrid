package credentials

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/ssm/types"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/aws/eks-hybrid/internal/api"
	"github.com/aws/eks-hybrid/internal/creds"
	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/constants"
)

const ssmActivationName = "eks-hybrid-ssm-provider"

type SsmProvider struct {
	activationId string
	SSM          *ssm.Client
	Role         string
}

func (s *SsmProvider) Name() creds.CredentialProvider {
	return creds.SsmCredentialProvider
}

func (s *SsmProvider) NodeadmConfig(ctx context.Context, node e2e.NodeSpec) (*api.NodeConfig, error) {
	ssmActivationDetails, err := createSSMActivation(ctx, s.SSM, s.Role, ssmActivationName, node.Cluster.Name)
	if err != nil {
		return nil, err
	}
	s.activationId = *ssmActivationDetails.ActivationId
	return &api.NodeConfig{
		TypeMeta: metav1.TypeMeta{
			APIVersion: "node.eks.aws/v1alpha1",
			Kind:       "NodeConfig",
		},
		Spec: api.NodeConfigSpec{
			Cluster: api.ClusterDetails{
				Name:   node.Cluster.Name,
				Region: node.Cluster.Region,
			},
			Hybrid: &api.HybridOptions{
				SSM: &api.SSM{
					ActivationID:   *ssmActivationDetails.ActivationId,
					ActivationCode: *ssmActivationDetails.ActivationCode,
				},
				EnableCredentialsFile: true,
			},
		},
	}, nil
}

func (s *SsmProvider) VerifyUninstall(ctx context.Context, instanceId string) error {
	return waitForManagedInstanceUnregistered(ctx, s.SSM, instanceId)
}

func (s *SsmProvider) FilesForNode(_ e2e.NodeSpec) ([]e2e.File, error) {
	return nil, nil
}

func (s *SsmProvider) NodeName(ctx context.Context) (string, error) {
	output, err := s.SSM.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
		Filters: []types.InstanceInformationStringFilter{
			{
				Key:    aws.String("ActivationIds"),
				Values: []string{s.activationId},
			},
			{
				Key:    aws.String("ResourceType"),
				Values: []string{"ManagedInstance"},
			},
		},
	})
	if err != nil {
		return "", err
	}
	if len(output.InstanceInformationList) == 0 {
		return "", fmt.Errorf("no managed instances found for ActivationId: %s", s.activationId)
	}
	return *output.InstanceInformationList[0].InstanceId, nil
}

func createSSMActivation(ctx context.Context, client *ssm.Client, iamRole, ssmActivationName, clusterName string) (*ssm.CreateActivationOutput, error) {
	// Define the input for the CreateActivation API
	input := &ssm.CreateActivationInput{
		IamRole:             aws.String(iamRole),
		RegistrationLimit:   aws.Int32(2),
		DefaultInstanceName: aws.String(ssmActivationName),
		Tags: []types.Tag{
			{
				Key:   aws.String(constants.TestClusterTagKey),
				Value: aws.String(clusterName),
			},
		},
	}

	// Call CreateActivation to create the SSM activation
	result, err := client.CreateActivation(ctx, input, func(o *ssm.Options) {
		o.RetryMaxAttempts = 20
		o.RetryMode = aws.RetryModeAdaptive
	})
	if err != nil {
		return nil, fmt.Errorf("creating SSM activation: %v", err)
	}

	return result, nil
}

func waitForManagedInstanceUnregistered(ctx context.Context, ssmClient *ssm.Client, instanceId string) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()

	statusCh := make(chan struct{})
	errCh := make(chan error)
	consecutiveErrors := 0

	go func() {
		defer close(statusCh)
		defer close(errCh)
		for {
			output, err := ssmClient.DescribeInstanceInformation(ctx, &ssm.DescribeInstanceInformationInput{
				Filters: []types.InstanceInformationStringFilter{
					{
						Key:    aws.String("InstanceIds"),
						Values: []string{instanceId},
					},
				},
			})
			if err != nil {
				consecutiveErrors += 1
				if consecutiveErrors > 3 || ctx.Err() != nil {
					errCh <- fmt.Errorf("failed to describe instance information %s: %v", instanceId, err)
					return
				}
			} else if len(output.InstanceInformationList) == 0 {
				statusCh <- struct{}{}
				return
			} else {
				consecutiveErrors = 0
			}

			time.Sleep(5 * time.Second)
		}
	}()

	select {
	case <-statusCh:
		return nil
	case err := <-errCh:
		return err
	case <-ctx.Done():
		return fmt.Errorf("timed out waiting for instance to unregister: %s", instanceId)
	}
}
