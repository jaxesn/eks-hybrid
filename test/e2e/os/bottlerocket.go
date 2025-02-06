package os

import (
	"context"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/aws/eks-hybrid/test/e2e"
)

//go:embed testdata/bottlerocket/settings.toml
var brSettingsToml []byte

type brSettingsTomlInitData struct {
	e2e.UserDataInput
	NodeadmUrl             string
	AdminContainerUserData string
	AWSConfig              string
}

type BottleRocket struct {
	amiArchitecture string
	architecture    architecture
}

func NewBottleRocket() *BottleRocket {
	br := new(BottleRocket)
	br.amiArchitecture = x8664Arch
	br.architecture = amd64
	return br
}

func NewBottleRocketARM() *BottleRocket {
	br := new(BottleRocket)
	br.amiArchitecture = arm64Arch
	br.architecture = arm64
	return br
}

func (a BottleRocket) Name() string {
	return "br-" + a.architecture.String()
}

func (a BottleRocket) InstanceType(region string) string {
	return getInstanceTypeFromRegionAndArch(region, a.architecture)
}

func (a BottleRocket) AMIName(ctx context.Context, awsConfig aws.Config) (string, error) {
	amiId, err := getAmiIDFromSSM(ctx, ssm.NewFromConfig(awsConfig), "/aws/service/bottlerocket/aws-k8s-1.31/"+a.amiArchitecture+"/latest/image_id")
	return *amiId, err
}

func (a BottleRocket) BuildUserData(userDataInput e2e.UserDataInput) ([]byte, error) {
	if err := populateBaseScripts(&userDataInput); err != nil {
		return nil, err
	}
	sshData := map[string]interface{}{
		"user":          "ec2-user",
		"password-hash": userDataInput.RootPasswordHash,
		"ssh": map[string][]string{
			"authorized-keys": {
				strings.TrimSuffix(userDataInput.PublicKey, "\n"),
			},
		},
	}

	jsonData, err := json.Marshal(sshData)
	if err != nil {
		return nil, err
	}
	sshKey := base64.StdEncoding.EncodeToString([]byte(jsonData))


	[default]
	region = us-west-2
	credential_process = aws_signing_helper credential-process --certificate /var/lib/kubelet/pki/kubelet-client-current.pem --private-key /var/lib/kubelet/pki/kubelet-client-current.pem --profile-arn [profile ARN]
	   --role-arn [role ARN]
	   --trust-anchor-arn [trust anchor ARN]

	awsConfig := fmt.Sprintf(`
	[default]
		region = us-west-2
		credential_process = aws_signing_helper credential-process 
			--certificate /var/lib/eks-hybrid/roles-anywhere/pki/node.crt
			--private-key /var/lib/eks-hybrid/roles-anywhere/pki/node.key
			--profile-arn %s
			--role-arn %s
			--trust-anchor-arn %s
   `, userDataInput.NodeadmConfig.Spec.Hybrid.IAMRolesAnywhere.ProfileARN, userDataInput.NodeadmConfig.Spec.Hybrid.IAMRolesAnywhere.RoleARN, userDataInput.NodeadmConfig.Spec.Hybrid.IAMRolesAnywhere.TrustAnchorARN)

	data := brSettingsTomlInitData{
		UserDataInput:          userDataInput,
		NodeadmUrl:             userDataInput.NodeadmUrls.AMD,
		AdminContainerUserData: sshKey,
		AWSConfig:              base64.StdEncoding.EncodeToString([]byte(awsConfig)),
	}

	if a.architecture.arm() {
		data.NodeadmUrl = userDataInput.NodeadmUrls.ARM
	}

	return executeTemplate(brSettingsToml, data)
}
