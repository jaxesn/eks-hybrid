package cleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/smithy-go"
	"github.com/go-logr/logr"
	. "github.com/onsi/gomega"

	"github.com/aws/eks-hybrid/test/e2e/cleanup"
	"github.com/aws/eks-hybrid/test/e2e/constants"
)

var errStackNotFound = smithy.GenericAPIError{
	Code:    "ValidationError",
	Message: "does not exist",
}

type mockCFNClient struct {
	describeStacksOutput *cloudformation.DescribeStacksOutput
	describeStacksError  error
	deleteStackError     error
	listStacksOutput     *cloudformation.ListStacksOutput
	listStacksError      error
}

func (m *mockCFNClient) DescribeStacks(ctx context.Context, params *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error) {
	if m.describeStacksOutput == nil {
		return nil, &errStackNotFound
	}

	if m.describeStacksError != nil {
		return nil, m.describeStacksError
	}

	// Filter stacks based on the requested stack name
	var filteredStacks []types.Stack
	requestedStackName := aws.ToString(params.StackName)
	for _, stack := range m.describeStacksOutput.Stacks {
		if requestedStackName != "" && aws.ToString(stack.StackName) == requestedStackName {
			filteredStacks = append(filteredStacks, stack)
			break
		}
	}

	if len(filteredStacks) == 0 && requestedStackName != "" {
		return nil, &errStackNotFound
	}

	return &cloudformation.DescribeStacksOutput{
		Stacks: filteredStacks,
	}, nil
}

func (m *mockCFNClient) DeleteStack(ctx context.Context, params *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error) {
	return &cloudformation.DeleteStackOutput{}, m.deleteStackError
}

func (m *mockCFNClient) ListStacks(ctx context.Context, params *cloudformation.ListStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ListStacksOutput, error) {
	return m.listStacksOutput, m.listStacksError
}

func (m *mockCFNClient) DescribeStackEvents(ctx context.Context, params *cloudformation.DescribeStackEventsInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error) {
	return &cloudformation.DescribeStackEventsOutput{}, nil
}

func TestListCredentialStacks(t *testing.T) {
	g := NewGomegaWithT(t)
	ctx := context.Background()
	logger := logr.Discard()

	const (
		testClusterPrefix   = "test-cluster-"
		testClusterName     = "test-cluster-1"
		testClusterName2    = "test-cluster-2"
		testClusterNameProd = "prod-cluster"
	)

	now := time.Now()
	oldTime := now.Add(-25 * time.Hour) // 25 hours old
	newTime := now.Add(-23 * time.Hour) // 23 hours old
	threshold := 24 * time.Hour

	// Define mock data once
	mockStacks := &mockCFNClient{
		listStacksOutput: &cloudformation.ListStacksOutput{
			StackSummaries: []types.StackSummary{
				// Credential stacks
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("cred-stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("cred-stack2"),
					CreationTime: aws.Time(newTime),
				},
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterNameProd),
					StackId:      aws.String("cred-stack3"),
					CreationTime: aws.Time(oldTime),
				},
				// Architecture stacks (should be filtered out)
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("arch-stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("arch-stack2"),
					CreationTime: aws.Time(oldTime),
				},
			},
		},
		describeStacksOutput: &cloudformation.DescribeStacksOutput{
			Stacks: []types.Stack{
				// Credential stacks
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("cred-stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName2),
					StackId:   aws.String("cred-stack2"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName2),
						},
					},
				},
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterNameProd),
					StackId:   aws.String("cred-stack3"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterNameProd),
						},
					},
				},
				// Architecture stacks (should be filtered out)
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("arch-stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName2),
					StackId:   aws.String("arch-stack2"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName2),
						},
					},
				},
			},
		},
	}

	// Mock client for stack not found case
	mockStacksWithNotFound := &mockCFNClient{
		listStacksOutput: &cloudformation.ListStacksOutput{
			StackSummaries: []types.StackSummary{
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("stack2"),
					CreationTime: aws.Time(oldTime),
				},
			},
		},
		describeStacksOutput: &cloudformation.DescribeStacksOutput{
			Stacks: []types.Stack{
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
			},
		},
	}

	// Mock client for list error case
	mockStacksWithListError := &mockCFNClient{
		listStacksError: errors.New("list stacks failed"),
	}

	tests := []struct {
		name           string
		client         *mockCFNClient
		input          cleanup.FilterInput
		expectedStacks []string
		expectedError  string
	}{
		{
			name:   "successfully lists credential stacks with exact cluster name",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "successfully lists credential stacks with cluster prefix",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterNamePrefix:    testClusterPrefix,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "successfully lists all credential stacks",
			client: mockStacks,
			input: cleanup.FilterInput{
				AllClusters:          true,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
				constants.TestCredentialsStackNamePrefix + "-" + testClusterNameProd,
			},
		},
		{
			name:   "filters out stacks newer than age threshold",
			client: mockStacks,
			input: cleanup.FilterInput{
				AllClusters:          true,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
				constants.TestCredentialsStackNamePrefix + "-" + testClusterNameProd,
			},
		},
		{
			name:   "combines cluster name and age threshold filters",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "skips stacks that are not found during describe",
			client: mockStacksWithNotFound,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestCredentialsStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "handles list stacks error",
			client: mockStacksWithListError,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedError: "describing instances: list stacks failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaner := cleanup.NewCFNStackCleanup(tt.client, logger)
			stacks, err := cleaner.ListCredentialStacks(ctx, tt.input)

			if tt.expectedError != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tt.expectedError)))
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(stacks).To(Equal(tt.expectedStacks))
		})
	}
}

func TestDeleteStack(t *testing.T) {
	g := NewGomegaWithT(t)
	ctx := context.Background()
	logger := logr.Discard()

	const testClusterName = "test-cluster"

	tests := []struct {
		name          string
		client        *mockCFNClient
		stackName     string
		expectedError string
	}{
		{
			name: "successfully deletes stack",
			client: &mockCFNClient{
				describeStacksOutput: &cloudformation.DescribeStacksOutput{
					Stacks: []types.Stack{
						{
							StackName:   aws.String("test-stack"),
							StackStatus: types.StackStatusCreateComplete,
							Tags: []types.Tag{
								{
									Key:   aws.String(constants.TestClusterTagKey),
									Value: aws.String(testClusterName),
								},
							},
						},
					},
				},
			},
			stackName: "test-stack",
		},
		{
			name:      "handles already deleted stack",
			client:    &mockCFNClient{},
			stackName: "test-stack",
		},
		{
			name: "handles describe stacks error",
			client: &mockCFNClient{
				describeStacksError: errors.New("internal error"),
			},
			stackName:     "test-stack",
			expectedError: "deleting hybrid nodes cfn stack: internal error",
		},
		{
			name: "handles delete stack error",
			client: &mockCFNClient{
				describeStacksOutput: &cloudformation.DescribeStacksOutput{
					Stacks: []types.Stack{
						{
							StackName:   aws.String("test-stack"),
							StackStatus: types.StackStatusCreateComplete,
							Tags: []types.Tag{
								{
									Key:   aws.String(constants.TestClusterTagKey),
									Value: aws.String(testClusterName),
								},
							},
						},
					},
				},
				deleteStackError: &types.StackNotFoundException{},
			},
			stackName:     "test-stack",
			expectedError: "deleting hybrid nodes cfn stack: stack not found",
		},
		{
			name: "handles delete failed stack",
			client: &mockCFNClient{
				describeStacksOutput: &cloudformation.DescribeStacksOutput{
					Stacks: []types.Stack{
						{
							StackName:   aws.String("test-stack"),
							StackStatus: types.StackStatusDeleteFailed,
							Tags: []types.Tag{
								{
									Key:   aws.String(constants.TestClusterTagKey),
									Value: aws.String(testClusterName),
								},
							},
						},
					},
				},
			},
			stackName: "test-stack",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaner := cleanup.NewCFNStackCleanup(tt.client, logger)
			err := cleaner.DeleteStack(ctx, tt.stackName)

			if tt.expectedError != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tt.expectedError)))
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
		})
	}
}

func TestListArchitectureStacks(t *testing.T) {
	g := NewGomegaWithT(t)
	ctx := context.Background()
	logger := logr.Discard()

	const (
		testClusterName     = "test-cluster"
		testClusterPrefix   = "test-cluster-"
		testClusterName2    = "test-cluster-2"
		testClusterNameProd = "prod-cluster"
	)

	now := time.Now()
	oldTime := now.Add(-25 * time.Hour) // 25 hours old
	newTime := now.Add(-23 * time.Hour) // 23 hours old
	threshold := 24 * time.Hour

	// Define mock data once
	mockStacks := &mockCFNClient{
		listStacksOutput: &cloudformation.ListStacksOutput{
			StackSummaries: []types.StackSummary{
				// Architecture stacks
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("arch-stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("arch-stack2"),
					CreationTime: aws.Time(newTime),
				},
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterNameProd),
					StackId:      aws.String("arch-stack3"),
					CreationTime: aws.Time(oldTime),
				},
				// Credential stacks (should be filtered out)
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("cred-stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("cred-stack2"),
					CreationTime: aws.Time(oldTime),
				},
			},
		},
		describeStacksOutput: &cloudformation.DescribeStacksOutput{
			Stacks: []types.Stack{
				// Architecture stacks
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("arch-stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName2),
					StackId:   aws.String("arch-stack2"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName2),
						},
					},
				},
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterNameProd),
					StackId:   aws.String("arch-stack3"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterNameProd),
						},
					},
				},
				// Credential stacks (should be filtered out)
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("cred-stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
				{
					StackName: aws.String(constants.TestCredentialsStackNamePrefix + "-" + testClusterName2),
					StackId:   aws.String("cred-stack2"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName2),
						},
					},
				},
			},
		},
	}

	// Mock client for stack not found case
	mockStacksWithNotFound := &mockCFNClient{
		listStacksOutput: &cloudformation.ListStacksOutput{
			StackSummaries: []types.StackSummary{
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:      aws.String("stack1"),
					CreationTime: aws.Time(oldTime),
				},
				{
					StackName:    aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName2),
					StackId:      aws.String("stack2"),
					CreationTime: aws.Time(oldTime),
				},
			},
		},
		describeStacksOutput: &cloudformation.DescribeStacksOutput{
			Stacks: []types.Stack{
				{
					StackName: aws.String(constants.TestArchitectureStackNamePrefix + "-" + testClusterName),
					StackId:   aws.String("stack1"),
					Tags: []types.Tag{
						{
							Key:   aws.String(constants.TestClusterTagKey),
							Value: aws.String(testClusterName),
						},
					},
				},
			},
		},
	}

	// Mock client for list error case
	mockStacksWithListError := &mockCFNClient{
		listStacksError: errors.New("list stacks failed"),
	}

	tests := []struct {
		name           string
		client         *mockCFNClient
		input          cleanup.FilterInput
		expectedStacks []string
		expectedError  string
	}{
		{
			name:   "successfully lists architecture stacks with exact cluster name",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "successfully lists architecture stacks with cluster prefix",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterName:          testClusterPrefix,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "successfully lists all architecture stacks",
			client: mockStacks,
			input: cleanup.FilterInput{
				AllClusters:          true,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
				constants.TestArchitectureStackNamePrefix + "-" + testClusterNameProd,
			},
		},
		{
			name:   "filters out stacks newer than age threshold",
			client: mockStacks,
			input: cleanup.FilterInput{
				AllClusters:          true,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
				constants.TestArchitectureStackNamePrefix + "-" + testClusterNameProd,
			},
		},
		{
			name:   "combines cluster prefix and age threshold filters",
			client: mockStacks,
			input: cleanup.FilterInput{
				ClusterName:          testClusterPrefix,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "skips stacks that are not found during describe",
			client: mockStacksWithNotFound,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedStacks: []string{
				constants.TestArchitectureStackNamePrefix + "-" + testClusterName,
			},
		},
		{
			name:   "handles list stacks error",
			client: mockStacksWithListError,
			input: cleanup.FilterInput{
				ClusterName:          testClusterName,
				InstanceAgeThreshold: threshold,
			},
			expectedError: "describing instances: list stacks failed",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cleaner := cleanup.NewCFNStackCleanup(tt.client, logger)
			stacks, err := cleaner.ListArchitectureStacks(ctx, tt.input)

			if tt.expectedError != "" {
				g.Expect(err).To(MatchError(ContainSubstring(tt.expectedError)))
				return
			}

			g.Expect(err).NotTo(HaveOccurred())
			g.Expect(stacks).To(Equal(tt.expectedStacks))
		})
	}
}
