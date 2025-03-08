package cleanup

import (
	"context"
	"fmt"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/resourcegroupstaggingapi"
	"github.com/go-logr/logr"

	"github.com/aws/eks-hybrid/test/e2e/constants"
)

// VPCCleaner is responsible for cleaning up AWS VPC resources
type VPCCleaner struct {
	EC2             *ec2.Client
	ResourceTagging *resourcegroupstaggingapi.Client
	Logger          logr.Logger
}

// FilterInput is defined in resource.go

// ListTaggedVPCs lists VPCs tagged with the cluster tag key
func (v *VPCCleaner) ListTaggedVPCs(ctx context.Context, input FilterInput) ([]string, error) {
	v.Logger.Info("Listing tagged VPCs")

	resourceTagger := ResourceTagger{
		ResourceTagging: v.ResourceTagging,
		ClusterName:     input.ClusterName,
	}
	parameterARNs, err := resourceTagger.GetTaggedResources(ctx, "ec2:vpc")
	if err ! 
	vpcs, err := v.EC2.DescribeVpcs(ctx, &ec2.DescribeVpcsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("tag-key"),
				Values: []string{constants.TestClusterTagKey},
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("describing VPCs: %w", err)
	}

	var vpcIDs []string
	for _, vpc := range vpcs.Vpcs {
		tags := convertEC2Tags(vpc.Tags)
		resource := ResourceWithTags{
			ID:   aws.ToString(vpc.VpcId),
			Tags: tags,
			// For VPCs we don't have a direct creation time, so we use a zero time
			// This means filtering by age will rely only on tags
			CreationTime: time.Time{},
		}

		if shouldDeleteResource(resource, input) {
			vpcIDs = append(vpcIDs, aws.ToString(vpc.VpcId))
		}
	}

	return vpcIDs, nil
}

// DeleteVPCs deletes the specified VPCs and their associated resources
func (v *VPCCleaner) DeleteVPCs(ctx context.Context, vpcIDs []string) error {
	if len(vpcIDs) == 0 {
		return nil
	}

	v.Logger.Info("Deleting VPCs", "vpcIDs", vpcIDs)

	for _, vpcID := range vpcIDs {
		// Step 1: Detach and delete Internet Gateways
		if err := v.deleteInternetGateways(ctx, vpcID); err != nil {
			return fmt.Errorf("deleting internet gateways: %w", err)
		}

		// Step 2: Delete Subnets
		if err := v.deleteSubnets(ctx, vpcID); err != nil {
			return fmt.Errorf("deleting subnets: %w", err)
		}

		// Step 3: Delete Route Tables (except main)
		if err := v.deleteRouteTables(ctx, vpcID); err != nil {
			return fmt.Errorf("deleting route tables: %w", err)
		}

		// Step 4: Delete Security Groups (except default)
		if err := v.deleteSecurityGroups(ctx, vpcID); err != nil {
			return fmt.Errorf("deleting security groups: %w", err)
		}

		// Step 5: Delete the VPC itself
		if err := v.deleteVPC(ctx, vpcID); err != nil {
			return fmt.Errorf("deleting VPC: %w", err)
		}
	}

	return nil
}

// deleteInternetGateways detaches and deletes internet gateways associated with a VPC
func (v *VPCCleaner) deleteInternetGateways(ctx context.Context, vpcID string) error {
	resp, err := v.EC2.DescribeInternetGateways(ctx, &ec2.DescribeInternetGatewaysInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("attachment.vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing internet gateways: %w", err)
	}

	for _, igw := range resp.InternetGateways {
		igwID := aws.ToString(igw.InternetGatewayId)
		v.Logger.Info("Detaching internet gateway", "igwID", igwID, "vpcID", vpcID)

		_, err := v.EC2.DetachInternetGateway(ctx, &ec2.DetachInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
			VpcId:             aws.String(vpcID),
		})
		if err != nil {
			return fmt.Errorf("detaching internet gateway %s: %w", igwID, err)
		}

		v.Logger.Info("Deleting internet gateway", "igwID", igwID)
		_, err = v.EC2.DeleteInternetGateway(ctx, &ec2.DeleteInternetGatewayInput{
			InternetGatewayId: igw.InternetGatewayId,
		})
		if err != nil {
			return fmt.Errorf("deleting internet gateway %s: %w", igwID, err)
		}
	}

	return nil
}

// deleteSubnets deletes all subnets in a VPC
func (v *VPCCleaner) deleteSubnets(ctx context.Context, vpcID string) error {
	resp, err := v.EC2.DescribeSubnets(ctx, &ec2.DescribeSubnetsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing subnets: %w", err)
	}

	for _, subnet := range resp.Subnets {
		subnetID := aws.ToString(subnet.SubnetId)
		v.Logger.Info("Deleting subnet", "subnetID", subnetID)

		_, err := v.EC2.DeleteSubnet(ctx, &ec2.DeleteSubnetInput{
			SubnetId: subnet.SubnetId,
		})
		if err != nil {
			return fmt.Errorf("deleting subnet %s: %w", subnetID, err)
		}
	}

	return nil
}

// deleteRouteTables deletes all route tables in a VPC except the main one
func (v *VPCCleaner) deleteRouteTables(ctx context.Context, vpcID string) error {
	// Get the main route table
	mainResp, err := v.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
			{
				Name:   aws.String("association.main"),
				Values: []string{"true"},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing main route table: %w", err)
	}

	var mainRouteTableID string
	if len(mainResp.RouteTables) > 0 {
		mainRouteTableID = aws.ToString(mainResp.RouteTables[0].RouteTableId)
	}

	// Get all route tables
	resp, err := v.EC2.DescribeRouteTables(ctx, &ec2.DescribeRouteTablesInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing route tables: %w", err)
	}

	for _, rt := range resp.RouteTables {
		rtID := aws.ToString(rt.RouteTableId)

		// Skip the main route table
		if rtID == mainRouteTableID {
			continue
		}

		// Disassociate any subnet associations first
		for _, assoc := range rt.Associations {
			if assoc.AssociationId != nil {
				v.Logger.Info("Disassociating route table", "rtID", rtID, "associationID", aws.ToString(assoc.AssociationId))
				_, err := v.EC2.DisassociateRouteTable(ctx, &ec2.DisassociateRouteTableInput{
					AssociationId: assoc.AssociationId,
				})
				if err != nil {
					return fmt.Errorf("disassociating route table %s: %w", rtID, err)
				}
			}
		}

		v.Logger.Info("Deleting route table", "rtID", rtID)
		_, err = v.EC2.DeleteRouteTable(ctx, &ec2.DeleteRouteTableInput{
			RouteTableId: rt.RouteTableId,
		})
		if err != nil {
			return fmt.Errorf("deleting route table %s: %w", rtID, err)
		}
	}

	return nil
}

// deleteSecurityGroups deletes all security groups in a VPC except the default one
func (v *VPCCleaner) deleteSecurityGroups(ctx context.Context, vpcID string) error {
	resp, err := v.EC2.DescribeSecurityGroups(ctx, &ec2.DescribeSecurityGroupsInput{
		Filters: []types.Filter{
			{
				Name:   aws.String("vpc-id"),
				Values: []string{vpcID},
			},
		},
	})
	if err != nil {
		return fmt.Errorf("describing security groups: %w", err)
	}

	// First, delete all rules from security groups to avoid dependency issues
	for _, sg := range resp.SecurityGroups {
		sgID := aws.ToString(sg.GroupId)
		sgName := aws.ToString(sg.GroupName)

		// Skip the default security group
		if sgName == "default" {
			continue
		}

		// Delete ingress rules
		if len(sg.IpPermissions) > 0 {
			_, err := v.EC2.RevokeSecurityGroupIngress(ctx, &ec2.RevokeSecurityGroupIngressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissions,
			})
			if err != nil {
				v.Logger.Info("Error revoking ingress rules", "sgID", sgID, "error", err)
				// Continue anyway as we'll try to delete the SG
			}
		}

		// Delete egress rules
		if len(sg.IpPermissionsEgress) > 0 {
			_, err := v.EC2.RevokeSecurityGroupEgress(ctx, &ec2.RevokeSecurityGroupEgressInput{
				GroupId:       sg.GroupId,
				IpPermissions: sg.IpPermissionsEgress,
			})
			if err != nil {
				v.Logger.Info("Error revoking egress rules", "sgID", sgID, "error", err)
				// Continue anyway as we'll try to delete the SG
			}
		}
	}

	// Now delete the security groups
	for _, sg := range resp.SecurityGroups {
		sgID := aws.ToString(sg.GroupId)
		sgName := aws.ToString(sg.GroupName)

		// Skip the default security group
		if sgName == "default" {
			continue
		}

		v.Logger.Info("Deleting security group", "sgID", sgID, "sgName", sgName)
		_, err := v.EC2.DeleteSecurityGroup(ctx, &ec2.DeleteSecurityGroupInput{
			GroupId: sg.GroupId,
		})
		if err != nil {
			return fmt.Errorf("deleting security group %s (%s): %w", sgID, sgName, err)
		}
	}

	return nil
}

// deleteVPC deletes the VPC itself
func (v *VPCCleaner) deleteVPC(ctx context.Context, vpcID string) error {
	v.Logger.Info("Deleting VPC", "vpcID", vpcID)

	_, err := v.EC2.DeleteVpc(ctx, &ec2.DeleteVpcInput{
		VpcId: aws.String(vpcID),
	})
	if err != nil {
		return fmt.Errorf("deleting VPC %s: %w", vpcID, err)
	}

	return nil
}

// Helper function to convert EC2 tags to our Tag structure
func convertEC2Tags(ec2Tags []types.Tag) []Tag {
	tags := make([]Tag, 0, len(ec2Tags))
	for _, tag := range ec2Tags {
		tags = append(tags, Tag{
			Key:   aws.ToString(tag.Key),
			Value: aws.ToString(tag.Value),
		})
	}
	return tags
}
