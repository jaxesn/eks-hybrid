package cfn

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"k8s.io/apimachinery/pkg/util/wait"

	"github.com/aws/eks-hybrid/test/e2e/errors"
)

type CFNClient interface {
	DescribeStacks(ctx context.Context, params *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
	DescribeStackEvents(ctx context.Context, params *cloudformation.DescribeStackEventsInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error)
	DeleteStack(ctx context.Context, params *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
	ListStacks(ctx context.Context, params *cloudformation.ListStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ListStacksOutput, error)
}

func GetStackFailureReason(ctx context.Context, client CFNClient, stackName string) (string, error) {
	resp, err := client.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{
		StackName: &stackName,
	})
	if err != nil {
		return "", fmt.Errorf("describing events for stack %s: %w", stackName, err)
	}
	firstFailedEventTimestamp := time.Now()
	var firstFailedEventReason string
	for _, event := range resp.StackEvents {
		if event.ResourceStatus == types.ResourceStatusCreateFailed ||
			event.ResourceStatus == types.ResourceStatusUpdateFailed ||
			event.ResourceStatus == types.ResourceStatusDeleteFailed {
			if event.ResourceStatusReason == nil {
				continue
			}

			timestamp := aws.ToTime(event.Timestamp)
			if timestamp.Before(firstFailedEventTimestamp) {
				firstFailedEventTimestamp = timestamp

				var resourceID string
				if event.LogicalResourceId != nil {
					resourceID = *event.LogicalResourceId
				} else {
					resourceID = "UnknownResource"
				}
				firstFailedEventReason = fmt.Sprintf("%s for %s: %s", event.ResourceStatus, resourceID, *event.ResourceStatusReason)
			}
		}
	}

	return firstFailedEventReason, nil
}

// WaitForStackOperation waits for a stack to reach Create/Update/Delete Complete
// when the operation fails, it will attempt to gather the failure reason and include it in the error
func WaitForStackOperation(ctx context.Context, client CFNClient, stackName string, stackWaitInterval, stackWaitTimeout time.Duration) error {
	err := wait.PollUntilContextTimeout(ctx, stackWaitInterval, stackWaitTimeout, true, func(ctx context.Context) (bool, error) {
		stackOutput, err := client.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{
			StackName: aws.String(stackName),
		})
		if err != nil {
			if errors.IsCFNStackNotFound(err) {
				return true, nil
			}
			return false, err
		}

		stackStatus := stackOutput.Stacks[0].StackStatus
		switch stackStatus {
		case types.StackStatusCreateComplete, types.StackStatusUpdateComplete, types.StackStatusDeleteComplete:
			return true, nil
		case types.StackStatusCreateInProgress, types.StackStatusUpdateInProgress, types.StackStatusDeleteInProgress, types.StackStatusUpdateCompleteCleanupInProgress:
			return false, nil
		default:
			failureReason, err := GetStackFailureReason(ctx, client, stackName)
			if err != nil {
				return false, fmt.Errorf("stack %s failed with status %s. Failed getting failure reason: %w", stackName, stackStatus, err)
			}
			return false, fmt.Errorf("stack %s failed with status: %s. Potential root cause: [%s]", stackName, stackStatus, failureReason)
		}
	})

	return err
}
