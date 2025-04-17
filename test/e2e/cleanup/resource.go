package cleanup

import (
	"strings"
	"time"

	"github.com/aws/eks-hybrid/test/e2e/constants"
)

type Tag struct {
	Key   string
	Value string
}

type ResourceWithTags struct {
	ID           string
	CreationTime time.Time
	Tags         []Tag
}

func getClusterTagValue(tags []Tag) string {
	var clusterTagValue string
	for _, tag := range tags {
		if tag.Key == constants.TestClusterTagKey {
			clusterTagValue = tag.Value
			break
		}
	}
	return clusterTagValue
}

func shouldDeleteResource(resource ResourceWithTags, input FilterInput) bool {
	clusterTagValue := getClusterTagValue(resource.Tags)
	if clusterTagValue == "" {
		return false
	}

	// For exact cluster name match, delete regardless of age
	if input.ClusterName != "" {
		return clusterTagValue == input.ClusterName
	}

	// For all clusters or prefix match, check resource age
	if input.AllClusters || (input.ClusterNamePrefix != "" && strings.HasPrefix(clusterTagValue, input.ClusterNamePrefix)) {
		return resourceOldEnough(resource.CreationTime, input)
	}

	return false
}

// resourceOldEnough returns true if the resource is old enough to be deleted
// this is intded to be called before requesting resource tags, for resources
// that require a seperate api call to get tags
// shouldDeleteResource should still be used as the soure of truth for the final decision
func resourceOldEnough(creationTime time.Time, input FilterInput) bool {
	// For exact cluster name match, consider old enough regardless of age
	if input.ClusterName != "" {
		return true
	}

	resourceAge := time.Since(creationTime)
	return resourceAge > input.InstanceAgeThreshold
}
