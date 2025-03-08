package credentials

import (
	"bytes"
	"context"
	_ "embed"
	"errors"
	"fmt"
	"os"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfnTypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamTypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"
	"github.com/go-logr/logr"

	"github.com/aws/eks-hybrid/test/e2e/constants"
	e2errors "github.com/aws/eks-hybrid/test/e2e/errors"
)

const (
	stackCreationTimeout = 5 * time.Minute
	stackDeletionTimeout = 8 * time.Minute
	stackRetryDelay      = 5 * time.Second
)

//go:embed cfn-templates/hybrid-cfn.yaml
var cfnTemplateBody string

type Stack struct {
	ClusterName            string
	Name                   string
	ClusterArn             string
	CFN                    *cloudformation.Client
	IAM                    *iam.Client
	EKS                    *eks.Client
	IAMRolesAnywhereCACert []byte
}

type hybridCfnTemplateVars struct {
	IncludeRolesAnywhere bool
}

type StackOutput struct {
	EC2Role            string `json:"EC2Role"`
	InstanceProfileARN string `json:"instanceProfileARN"`
	SSMNodeRoleName    string `json:"ssmNodeRoleName"`
	SSMNodeRoleARN     string `json:"ssmNodeRoleARN"`
	IRANodeRoleName    string `json:"iraNodeRoleName"`
	IRANodeRoleARN     string `json:"iraNodeRoleARN"`
	IRATrustAnchorARN  string `json:"iraTrustAnchorARN"`
	IRAProfileARN      string `json:"iraProfileARN"`
}

func (s *Stack) Deploy(ctx context.Context, logger logr.Logger) (*StackOutput, error) {
	// There is a race when creating the iam roles anywhere profile in cfn
	// where the profile gets created, but cfn tries to get the tags before
	// the resource has been fully created, attempt 1 retry
	var err error
	for range 2 {
		if err = s.deployStack(ctx, logger); err == nil {
			break
		} else {
			logger.Error(err, "Error deploying stack, retrying")
		}
	}
	if err != nil {
		return nil, err
	}

	output, err := s.ReadStackOutput(ctx, logger)
	if err != nil {
		return nil, err
	}
	logger.Info("E2E resources stack deployed successfully", "stackName", s.Name)

	logger.Info("Creating access entry", "ssmRoleArn", output.SSMNodeRoleARN)
	_, err = s.EKS.CreateAccessEntry(ctx, &eks.CreateAccessEntryInput{
		ClusterName:  &s.ClusterName,
		PrincipalArn: &output.SSMNodeRoleARN,
		Type:         aws.String("HYBRID_LINUX"),
	})
	if err != nil && !isResourceAlreadyInUse(err) {
		return nil, err
	}
	logger.Info("Creating access entry", "iamRoleArn", output.IRANodeRoleARN)
	_, err = s.EKS.CreateAccessEntry(ctx, &eks.CreateAccessEntryInput{
		ClusterName:  &s.ClusterName,
		PrincipalArn: &output.IRANodeRoleARN,
		Type:         aws.String("HYBRID_LINUX"),
	})
	if err != nil && !isResourceAlreadyInUse(err) {
		return nil, err
	}

	// We create the instance profile manually instead of as part of the CFN stack because it's faster.
	// This sucks because of the complexity it adds both to create and delete, having to deal with
	// partial creations where the instance profile might exist already and role might have been added or not.
	// But it speeds up the test about 2.5 minutes, so it's worth it. For some reason, creating
	// instance profiles from CFN
	// is very slow: https://repost.aws/questions/QUoU5UybeUR2S2iYNEJiStiQ/cloudformation-provisioning-of-aws-iam-instanceprofile-takes-a-long-time
	// I suspect this is because CFN has a hardcoded ~2.5 minutes wait after instance profile creation before
	// considering it "created". Probably to avoid eventual consistency issues when using the instance profile in
	// another resource immediately after. We have to deal with that problem ourselves now by retrying the ec2 instance
	// creation on "invalid IAM instance profile" error.
	output.InstanceProfileARN, err = s.createInstanceProfile(ctx, logger, output.EC2Role)
	if err != nil {
		return nil, err
	}

	return output, nil
}

func (s *Stack) deployStack(ctx context.Context, logger logr.Logger) error {
	resp, err := s.CFN.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(s.Name),
	})
	if err != nil && !e2errors.IsCFNStackNotFound(err) {
		return fmt.Errorf("looking for hybrid nodes cfn stack: %w", err)
	}
	params := []cfnTypes.Parameter{
		{
			ParameterKey:   aws.String("clusterName"),
			ParameterValue: aws.String(s.ClusterName),
		},
		{
			ParameterKey:   aws.String("clusterArn"),
			ParameterValue: aws.String(s.ClusterArn),
		},
		{
			ParameterKey:   aws.String("caBundleCert"),
			ParameterValue: aws.String(string(s.IAMRolesAnywhereCACert)),
		},
	}

	var buf bytes.Buffer
	cfnTemplate, err := template.New("hybridCfnTemplate").Parse(cfnTemplateBody)
	if err != nil {
		return fmt.Errorf("parsing hybrid-cfn.yaml template: %w", err)
	}
	cfnTemplateConfig := &hybridCfnTemplateVars{IncludeRolesAnywhere: !skipIRATest()}
	err = cfnTemplate.Execute(&buf, cfnTemplateConfig)
	if err != nil {
		return fmt.Errorf("applying data to hybrid-cfn.yaml template: %w", err)
	}
	if resp == nil || len(resp.Stacks) == 0 {
		logger.Info("Creating hybrid nodes stack", "stackName", s.Name)
		_, err = s.CFN.CreateStack(ctx, &cloudformation.CreateStackInput{
			DisableRollback: aws.Bool(true),
			StackName:       aws.String(s.Name),
			TemplateBody:    aws.String(buf.String()),
			Parameters:      params,
			Capabilities: []cfnTypes.Capability{
				"CAPABILITY_NAMED_IAM",
			},
			Tags: []cfnTypes.Tag{{
				Key:   aws.String(constants.TestClusterTagKey),
				Value: aws.String(s.ClusterName),
			}},
		})
		if err != nil {
			return fmt.Errorf("creating hybrid nodes cfn stack: %w", err)
		}
		if err := s.waitForStackCreation(ctx, logger); err != nil {
			return err
		}
	} else if resp.Stacks[0].StackStatus == cfnTypes.StackStatusCreateInProgress {
		if err := s.waitForStackCreation(ctx, logger); err != nil {
			return err
		}
	} else {
		logger.Info("Updating hybrid nodes stack", "stackName", s.Name)
		_, err = s.CFN.UpdateStack(ctx, &cloudformation.UpdateStackInput{
			DisableRollback: aws.Bool(true),
			StackName:       aws.String(s.Name),
			Capabilities: []cfnTypes.Capability{
				"CAPABILITY_NAMED_IAM",
			},
			TemplateBody: aws.String(buf.String()),
			Parameters:   params,
		})
		var apiErr smithy.APIError
		if ok := errors.As(err, &apiErr); err != nil && (!ok || apiErr.ErrorMessage() != "No updates are to be performed.") {
			return fmt.Errorf("updating hybrid nodes cfn stack: %w", err)
		} else if ok && apiErr.ErrorMessage() == "No updates are to be performed." {
			logger.Info("No updates are to be performed for hybrid nodes stack", "stackName", s.Name)
			// Skip waiting for update completion since no update occurred
			return nil
		}

		logger.Info("Waiting for hybrid nodes stack to be updated", "stackName", s.Name)
		waiter := cloudformation.NewStackUpdateCompleteWaiter(s.CFN, func(opts *cloudformation.StackUpdateCompleteWaiterOptions) {
			opts.MinDelay = stackRetryDelay
			opts.MaxDelay = stackRetryDelay
		})
		err = waiter.Wait(ctx, &cloudformation.DescribeStacksInput{
			StackName: aws.String(s.Name),
		}, stackCreationTimeout)
		if err != nil {
			return fmt.Errorf("waiting for hybrid nodes cfn stack: %w", err)
		}
	}

	return nil
}

func (s *Stack) createInstanceProfile(ctx context.Context, logger logr.Logger, roleName string) (instanceProfileArn string, err error) {
	instanceProfileName := s.instanceProfileName(roleName)

	instanceProfile, err := s.IAM.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
	})
	var instanceProfileHasRole bool
	if isNotFound(err) {
		logger.Info("Creating instance profile", "instanceProfileName", instanceProfileName)
		instanceProfileArnOut, err := s.IAM.CreateInstanceProfile(ctx, &iam.CreateInstanceProfileInput{
			InstanceProfileName: aws.String(instanceProfileName),
			Path:                aws.String("/"),
			Tags: []iamTypes.Tag{{
				Key:   aws.String(constants.TestClusterTagKey),
				Value: aws.String(s.ClusterName),
			}},
		})
		if err != nil {
			return "", err
		}
		instanceProfileArn = *instanceProfileArnOut.InstanceProfile.Arn
		instanceProfileHasRole = false
	} else if err != nil {
		return "", err
	} else {
		logger.Info("Instance profile already exists", "instanceProfileName", instanceProfileName)
		instanceProfileArn = *instanceProfile.InstanceProfile.Arn
		if len(instanceProfile.InstanceProfile.Roles) > 0 {
			instanceProfileHasRole = true
		} else {
			instanceProfileHasRole = false
		}
	}

	if instanceProfileHasRole {
		logger.Info("Instance profile already has a role", "instanceProfileName", instanceProfileName)
	} else {
		logger.Info("Adding role to instance profile", "roleName", roleName)
		_, err = s.IAM.AddRoleToInstanceProfile(ctx, &iam.AddRoleToInstanceProfileInput{
			InstanceProfileName: aws.String(instanceProfileName),
			RoleName:            aws.String(roleName),
		})
		if err != nil {
			return "", err
		}

	}

	return instanceProfileArn, nil
}

func (s *Stack) instanceProfileName(roleName string) string {
	return roleName
}

func (s *Stack) ReadStackOutput(ctx context.Context, logger logr.Logger) (*StackOutput, error) {
	resp, err := s.CFN.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(s.Name),
	})
	if err != nil {
		return nil, fmt.Errorf("describing hybrid nodes cfn stack: %w", err)
	}

	result := &StackOutput{}
	// extract relevant stack outputs
	for _, output := range resp.Stacks[0].Outputs {
		switch *output.OutputKey {
		case "EC2Role":
			result.EC2Role = *output.OutputValue
		case "SSMNodeRoleName":
			result.SSMNodeRoleName = *output.OutputValue
		case "SSMNodeRoleARN":
			result.SSMNodeRoleARN = *output.OutputValue
		case "IRANodeRoleName":
			result.IRANodeRoleName = *output.OutputValue
		case "IRANodeRoleARN":
			result.IRANodeRoleARN = *output.OutputValue
		case "IRATrustAnchorARN":
			result.IRATrustAnchorARN = *output.OutputValue
		case "IRAProfileARN":
			result.IRAProfileARN = *output.OutputValue
		}
	}

	return result, nil
}

func (s *Stack) Delete(ctx context.Context, logger logr.Logger, output *StackOutput) error {
	instanceProfileName := s.instanceProfileName(output.EC2Role)
	logger.Info("Deleting instance profile", "instanceProfileName", instanceProfileName)
	instanceProfile, err := s.IAM.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
	})
	if err != nil {
		return err
	}
	if _, err := s.IAM.RemoveRoleFromInstanceProfile(ctx, &iam.RemoveRoleFromInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
		RoleName:            instanceProfile.InstanceProfile.Roles[0].RoleName,
	}); err != nil {
		return err
	}
	if _, err := s.IAM.DeleteInstanceProfile(ctx, &iam.DeleteInstanceProfileInput{
		InstanceProfileName: aws.String(instanceProfileName),
	}); err != nil {
		return fmt.Errorf("deleting instance profile: %w", err)
	}

	logger.Info("Deleting access entry", "ssmRoleArn", output.SSMNodeRoleARN)
	if _, err := s.EKS.DeleteAccessEntry(ctx, &eks.DeleteAccessEntryInput{
		ClusterName:  &s.ClusterName,
		PrincipalArn: &output.SSMNodeRoleARN,
	}); err != nil {
		return fmt.Errorf("deleting SSM access entry: %w", err)
	}
	logger.Info("Deleting access entry", "iamRoleArn", output.IRANodeRoleARN)
	if _, err := s.EKS.DeleteAccessEntry(ctx, &eks.DeleteAccessEntryInput{
		ClusterName:  &s.ClusterName,
		PrincipalArn: &output.IRANodeRoleARN,
	}); err != nil {
		return fmt.Errorf("deleting iam-ra access entry: %w", err)
	}

	_, err = s.CFN.DeleteStack(ctx, &cloudformation.DeleteStackInput{
		StackName: aws.String(s.Name),
	})
	if err != nil {
		return fmt.Errorf("deleting hybrid nodes cfn stack: %w", err)
	}
	waiter := cloudformation.NewStackDeleteCompleteWaiter(s.CFN, func(opts *cloudformation.StackDeleteCompleteWaiterOptions) {
		opts.MinDelay = stackRetryDelay
		opts.MaxDelay = stackRetryDelay
	})
	err = waiter.Wait(ctx,
		&cloudformation.DescribeStacksInput{StackName: aws.String(s.Name)},
		stackDeletionTimeout)
	if err != nil {
		return fmt.Errorf("waiting for hybrid nodes cfn stack: %w", err)
	}
	logger.Info("E2E resources stack deleted successfully", "stackName", s.Name)
	return nil
}

func isNotFound(err error) bool {
	var awsErr smithy.APIError
	ok := errors.As(err, &awsErr)
	return err != nil && ok && awsErr.ErrorCode() == "NoSuchEntity"
}

func skipIRATest() bool {
	return os.Getenv("SKIP_IRA_TEST") == "true"
}

func (s *Stack) waitForStackCreation(ctx context.Context, logger logr.Logger) error {
	logger.Info("Waiting for hybrid nodes stack to be created", "stackName", s.Name)
	waiter := cloudformation.NewStackCreateCompleteWaiter(s.CFN, func(opts *cloudformation.StackCreateCompleteWaiterOptions) {
		opts.MinDelay = stackRetryDelay
		opts.MaxDelay = stackRetryDelay
	})
	err := waiter.Wait(ctx, &cloudformation.DescribeStacksInput{
		StackName: aws.String(s.Name),
	}, stackCreationTimeout)
	if err != nil {
		return fmt.Errorf("waiting for hybrid nodes cfn stack: %w", err)
	}
	return nil
}

func isResourceAlreadyInUse(err error) bool {
	var awsErr smithy.APIError
	ok := errors.As(err, &awsErr)
	return err != nil && ok && awsErr.ErrorCode() == "ResourceInUseException"
}
