package cleanup

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/rolesanywhere"
	"github.com/aws/aws-sdk-go-v2/service/rolesanywhere/types"
	"github.com/go-logr/logr"

	"github.com/aws/eks-hybrid/test/e2e/constants"
	"github.com/aws/eks-hybrid/test/e2e/errors"
)

type RolesAnywhereCleaner struct {
	rolesAnywhere *rolesanywhere.Client
	logger        logr.Logger
}

func NewRolesAnywhereCleaner(rolesAnywhere *rolesanywhere.Client, logger logr.Logger) *RolesAnywhereCleaner {
	return &RolesAnywhereCleaner{
		rolesAnywhere: rolesAnywhere,
		logger:        logger,
	}
}

func shouldDeleteProfile(profile types.ProfileDetail, tags []types.Tag, filterInput FilterInput) bool {
	var customTags []Tag
	for _, tag := range tags {
		customTags = append(customTags, Tag{
			Key:   *tag.Key,
			Value: *tag.Value,
		})
	}
	resource := ResourceWithTags{
		ID:           *profile.Name,
		CreationTime: aws.ToTime(profile.CreatedAt),
		Tags:         customTags,
	}

	return shouldDeleteResource(resource, filterInput)
}

func (c *RolesAnywhereCleaner) ListProfiles(ctx context.Context, filterInput FilterInput) ([]string, error) {
	var profiles []string

	// list profiles does not support tag filters so we pull all profiles
	// then describe to get the tags to filter out the ones that dont match
	// We have the role =* checks to try and limit which roles we bother checking tags for
	// but we only delete those with the e2e cluster tag
	paginator := rolesanywhere.NewListProfilesPaginator(c.rolesAnywhere, &rolesanywhere.ListProfilesInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing roles anywhere profiles: %w", err)
		}

		for _, profile := range page.Profiles {
			if !strings.HasPrefix(*profile.Name, constants.TestCredentialsStackNamePrefix) {
				continue
			}
			if !resourceOldEnough(aws.ToTime(profile.CreatedAt), filterInput) {
				continue
			}

			output, err := c.rolesAnywhere.ListTagsForResource(ctx, &rolesanywhere.ListTagsForResourceInput{
				ResourceArn: profile.ProfileArn,
			})

			if err != nil && errors.IsType(err, &types.ValidationException{}) {
				// skipping log since we are possiblying checking profiles we do not
				// intend to delete
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("listing roles anywhere profile tags: %w", err)
			}

			if shouldDeleteProfile(profile, output.Tags, filterInput) {
				profiles = append(profiles, *profile.ProfileId)
			}
		}
	}

	return profiles, nil
}

func (c *RolesAnywhereCleaner) DeleteProfile(ctx context.Context, profileID string) error {
	_, err := c.rolesAnywhere.DeleteProfile(ctx, &rolesanywhere.DeleteProfileInput{
		ProfileId: aws.String(profileID),
	})
	if err != nil && errors.IsType(err, &types.ResourceNotFoundException{}) {
		c.logger.Info("Roles Anywhere profile already deleted", "profileID", profileID)

		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting roles anywhere profile %s: %w", profileID, err)
	}
	c.logger.Info("Deleted Roles Anywhere profile", "profileID", profileID)
	return nil
}

func shouldDeleteTrustAnchor(anchor types.TrustAnchorDetail, tags []types.Tag, filterInput FilterInput) bool {
	var customTags []Tag
	for _, tag := range tags {
		customTags = append(customTags, Tag{
			Key:   *tag.Key,
			Value: *tag.Value,
		})
	}
	resource := ResourceWithTags{
		ID:           *anchor.Name,
		CreationTime: aws.ToTime(anchor.CreatedAt),
		Tags:         customTags,
	}

	return shouldDeleteResource(resource, filterInput)
}

func (c *RolesAnywhereCleaner) ListTrustAnchors(ctx context.Context, filterInput FilterInput) ([]string, error) {
	var anchors []string

	// list profiles does not support tag filters so we pull all profiles
	// then describe to get the tags to filter out the ones that dont match
	// We have the role =* checks to try and limit which roles we bother checking tags for
	// but we only delete those with the e2e cluster tag
	paginator := rolesanywhere.NewListTrustAnchorsPaginator(c.rolesAnywhere, &rolesanywhere.ListTrustAnchorsInput{})
	for paginator.HasMorePages() {
		page, err := paginator.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("listing roles anywhere profiles: %w", err)
		}

		for _, anchor := range page.TrustAnchors {
			if !strings.HasPrefix(*anchor.Name, constants.TestCredentialsStackNamePrefix) {
				continue
			}
			if !resourceOldEnough(aws.ToTime(anchor.CreatedAt), filterInput) {
				continue
			}

			output, err := c.rolesAnywhere.ListTagsForResource(ctx, &rolesanywhere.ListTagsForResourceInput{
				ResourceArn: anchor.TrustAnchorArn,
			})
			if err != nil && errors.IsType(err, &types.ValidationException{}) {
				// skipping log since we are possiblying checking trust anchors we do not
				// intend to delete
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("listing roles anywhere profile tags: %w", err)
			}

			if shouldDeleteTrustAnchor(anchor, output.Tags, filterInput) {
				anchors = append(anchors, *anchor.TrustAnchorId)
			}
		}
	}

	return anchors, nil
}

func (c *RolesAnywhereCleaner) DeleteTrustAnchor(ctx context.Context, anchorID string) error {
	_, err := c.rolesAnywhere.DeleteTrustAnchor(ctx, &rolesanywhere.DeleteTrustAnchorInput{
		TrustAnchorId: aws.String(anchorID),
	})
	if err != nil && errors.IsType(err, &types.ResourceNotFoundException{}) {
		c.logger.Info("Roles Anywhere trust anchor already deleted", "anchorID", anchorID)

		return nil
	}
	if err != nil {
		return fmt.Errorf("deleting roles anywhere trust anchor %s: %w", anchorID, err)
	}
	c.logger.Info("Deleted Roles Anywhere trust anchor", "anchorID", anchorID)
	return nil
}
