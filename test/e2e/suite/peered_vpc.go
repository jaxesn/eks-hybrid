package suite

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	ec2v2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/eks"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	s3v2 "github.com/aws/aws-sdk-go-v2/service/s3"
	ssmv2 "github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/go-logr/logr"
	. "github.com/onsi/ginkgo/v2"
	"github.com/onsi/ginkgo/v2/types"
	. "github.com/onsi/gomega"
	"gopkg.in/yaml.v2"
	clientgo "k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/addon"
	"github.com/aws/eks-hybrid/test/e2e/cluster"
	"github.com/aws/eks-hybrid/test/e2e/commands"
	"github.com/aws/eks-hybrid/test/e2e/constants"
	"github.com/aws/eks-hybrid/test/e2e/credentials"
	"github.com/aws/eks-hybrid/test/e2e/nodeadm"
	osystem "github.com/aws/eks-hybrid/test/e2e/os"
	"github.com/aws/eks-hybrid/test/e2e/peered"
	"github.com/aws/eks-hybrid/test/e2e/s3"
	"github.com/aws/eks-hybrid/test/e2e/ssm"
)

// notSupported is a collection of nodeadm config matchers for OS/Provider combinations
// that are not supported in the peered VPC test.
var notSupported = NodeadmConfigMatchers{}

type SuiteConfiguration struct {
	TestConfig             *e2e.TestConfig          `json:"testConfig"`
	SkipCleanup            bool                     `json:"skipCleanup"`
	CredentialsStackOutput *credentials.StackOutput `json:"ec2StackOutput"`
	RolesAnywhereCACertPEM []byte                   `json:"rolesAnywhereCACertPEM"`
	RolesAnywhereCAKeyPEM  []byte                   `json:"rolesAnywhereCAPrivateKeyPEM"`
	PublicKey              string                   `json:"publicKey"`
	JumpboxInstanceId      string                   `json:"jumpboxInstanceId"`
}

type PeeredVPCTest struct {
	aws             aws.Config
	eksEndpoint     string
	eksClient       *eks.Client
	ec2Client       *ec2v2.Client
	SSMClient       *ssmv2.Client
	cfnClient       *cloudformation.Client
	k8sClient       clientgo.Interface
	K8sClientConfig *rest.Config
	s3Client        *s3v2.Client
	iamClient       *iam.Client

	Logger        logr.Logger
	loggerControl e2e.PausableLogger
	logsBucket    string
	ArtifactsPath string

	Cluster         *peered.HybridCluster
	StackOut        *credentials.StackOutput
	nodeadmURLs     e2e.NodeadmURLs
	RolesAnywhereCA *credentials.Certificate

	OverrideNodeK8sVersion string
	setRootPassword        bool
	SkipCleanup            bool

	publicKey string

	RemoteCommandRunner commands.RemoteCommandRunner

	podIdentityS3Bucket string

	// failureMessageLogged tracks if a terminal error due to a failed gomega
	// expectation has already been registered and logged . It avoids logging
	// the same multiple times.
	failureMessageLogged bool
}

func BuildPeeredVPCTestForSuite(ctx context.Context, suite *SuiteConfiguration) (*PeeredVPCTest, error) {
	pausableLogger := NewLoggerForTests()
	test := &PeeredVPCTest{
		eksEndpoint:            suite.TestConfig.Endpoint,
		StackOut:               suite.CredentialsStackOutput,
		Logger:                 pausableLogger.Logger,
		loggerControl:          pausableLogger,
		logsBucket:             suite.TestConfig.LogsBucket,
		ArtifactsPath:          suite.TestConfig.ArtifactsFolder,
		OverrideNodeK8sVersion: suite.TestConfig.NodeK8sVersion,
		publicKey:              suite.PublicKey,
		setRootPassword:        suite.TestConfig.SetRootPassword,
		SkipCleanup:            suite.SkipCleanup,
	}

	aws, err := e2e.NewAWSConfig(ctx, awsconfig.WithRegion(suite.TestConfig.ClusterRegion),
		// We use a custom AppId so the requests show that they were
		// made by this test in the user-agent
		awsconfig.WithAppID("nodeadm-e2e-test"),
	)
	if err != nil {
		return nil, err
	}

	test.aws = aws
	test.eksClient = e2e.NewEKSClient(aws, suite.TestConfig.Endpoint)
	test.ec2Client = ec2v2.NewFromConfig(aws)
	test.SSMClient = ssmv2.NewFromConfig(aws)
	test.s3Client = s3v2.NewFromConfig(aws)
	test.cfnClient = cloudformation.NewFromConfig(aws)
	test.iamClient = iam.NewFromConfig(aws)
	test.RemoteCommandRunner = ssm.NewSSHOnSSMCommandRunner(test.SSMClient, suite.JumpboxInstanceId, test.Logger)

	ca, err := credentials.ParseCertificate(suite.RolesAnywhereCACertPEM, suite.RolesAnywhereCAKeyPEM)
	if err != nil {
		return nil, err
	}
	test.RolesAnywhereCA = ca

	// TODO: ideally this should be an input to the tests and not just
	// assume same name/path used by the setup command.
	clientConfig, err := clientcmd.BuildConfigFromFlags("", cluster.KubeconfigPath(suite.TestConfig.ClusterName))
	if err != nil {
		return nil, err
	}
	test.K8sClientConfig = clientConfig
	test.k8sClient, err = clientgo.NewForConfig(clientConfig)
	if err != nil {
		return nil, err
	}

	test.Cluster, err = peered.GetHybridCluster(ctx, test.eksClient, test.ec2Client, suite.TestConfig.ClusterName)
	if err != nil {
		return nil, err
	}

	urls, err := s3.BuildNodeamURLs(ctx, test.s3Client, suite.TestConfig.NodeadmUrlAMD, suite.TestConfig.NodeadmUrlARM)
	if err != nil {
		return nil, err
	}
	test.nodeadmURLs = *urls

	test.podIdentityS3Bucket, err = addon.PodIdentityBucket(ctx, test.s3Client, test.Cluster.Name)
	if err != nil {
		return nil, err
	}

	// override the default fail handler to print the error message immediately
	// following the error. We override here once the logger has been initialized
	// to ensure the error message is printed after the serial log (if it happens while waiting)
	RegisterFailHandler(test.handleFailure)

	return test, nil
}

func (t *PeeredVPCTest) NewPeeredNode() *peered.Node {
	return &peered.Node{
		NodeCreate: peered.NodeCreate{
			AWS:             t.aws,
			EC2:             t.ec2Client,
			SSM:             t.SSMClient,
			K8sClientConfig: t.K8sClientConfig,
			Logger:          t.Logger,
			Cluster:         t.Cluster,
			NodeadmURLs:     t.nodeadmURLs,
			PublicKey:       t.publicKey,
			SetRootPassword: t.setRootPassword,
		},
		NodeCleanup: peered.NodeCleanup{
			RemoteCommandRunner: t.RemoteCommandRunner,
			EC2:                 t.ec2Client,
			SSM:                 t.SSMClient,
			S3:                  t.s3Client,
			K8s:                 t.k8sClient,
			Logger:              t.Logger,
			SkipDelete:          t.SkipCleanup,
			ClusterName:         t.Cluster.Name,
			LogsBucket:          t.logsBucket,
		},
	}
}

func (t *PeeredVPCTest) NewCleanNode(provider e2e.NodeadmCredentialsProvider, nodeName, nodeIP string, os e2e.NodeadmOS) *nodeadm.CleanNode {
	return &nodeadm.CleanNode{
		K8s:                 t.k8sClient,
		RemoteCommandRunner: t.RemoteCommandRunner,
		OS:                  os,
		Verifier:            provider,
		Logger:              t.Logger,
		NodeName:            nodeName,
		NodeIP:              nodeIP,
	}
}

func (t *PeeredVPCTest) NewUpgradeNode(nodeName, nodeIP string) *nodeadm.UpgradeNode {
	return &nodeadm.UpgradeNode{
		K8s:                 t.k8sClient,
		RemoteCommandRunner: t.RemoteCommandRunner,
		Logger:              t.Logger,
		NodeName:            nodeName,
		NodeIP:              nodeIP,
		TargetK8sVersion:    t.Cluster.KubernetesVersion,
	}
}

func (t *PeeredVPCTest) InstanceName(testName string, os e2e.NodeadmOS, provider e2e.NodeadmCredentialsProvider) string {
	return fmt.Sprintf("EKSHybridCI-%s-%s-%s-%s",
		testName,
		e2e.SanitizeForAWSName(t.Cluster.Name),
		e2e.SanitizeForAWSName(os.Name()),
		e2e.SanitizeForAWSName(string(provider.Name())),
	)
}

func (t *PeeredVPCTest) NewVerifyPodIdentityAddon(nodeName, osName string) *addon.VerifyPodIdentityAddon {
	return &addon.VerifyPodIdentityAddon{
		Cluster:             t.Cluster.Name,
		NodeName:            nodeName,
		PodIdentityS3Bucket: t.podIdentityS3Bucket,
		K8S:                 t.k8sClient,
		EKSClient:           t.eksClient,
		IAMClient:           t.iamClient,
		S3Client:            t.s3Client,
		Logger:              t.Logger,
		K8SConfig:           t.K8sClientConfig,
		Region:              t.Cluster.Region,
		OS:                  osName,
	}
}

func (t *PeeredVPCTest) NewTestNode(ctx context.Context, instanceName, nodeName, k8sVersion string, os e2e.NodeadmOS, provider e2e.NodeadmCredentialsProvider, instanceSize e2e.InstanceSize) *testNode {
	return &testNode{
		ArtifactsPath:   t.ArtifactsPath,
		ClusterName:     t.Cluster.Name,
		EC2Client:       t.ec2Client,
		EKSEndpoint:     t.eksEndpoint,
		FailHandler:     t.handleFailure,
		InstanceName:    instanceName,
		InstanceSize:    instanceSize,
		Logger:          t.Logger,
		LoggerControl:   t.loggerControl,
		LogsBucket:      t.logsBucket,
		PeeredNode:      t.NewPeeredNode(),
		NodeName:        nodeName,
		K8sClient:       t.k8sClient,
		K8sClientConfig: t.K8sClientConfig,
		K8sVersion:      k8sVersion,
		OS:              os,
		Provider:        provider,
		Region:          t.Cluster.Region,
	}
}

// handleFailure is a wrapper around ginkgo.Fail that logs the error message
// immediately after it happens. It doesn't modify gomega's or ginkgo's regular
// behavior.
// We do this to help debug errors when going through the test logs.
func (t *PeeredVPCTest) handleFailure(message string, callerSkip ...int) {
	skip := 0
	if len(callerSkip) > 0 {
		skip = callerSkip[0]
	}
	if !t.failureMessageLogged {
		cl := types.NewCodeLocationWithStackTrace(skip + 1)
		err := types.GinkgoError{
			Message:      message,
			CodeLocation: cl,
		}
		t.Logger.Error(nil, err.Error())
		t.failureMessageLogged = true
	}
	Fail(message, skip+1)
}

func NewLoggerForTests() e2e.PausableLogger {
	_, reporter := GinkgoConfiguration()
	cfg := e2e.LoggerConfig{}
	if reporter.NoColor {
		cfg.NoColor = true
	}
	return e2e.NewPausableLogger(cfg)
}

// BeforeSuiteCredentialSetup is a helper function that creates the credential stack
// and returns a byte[] json representation of the SuiteConfiguration struct.
// This is intended to be used in SynchronizedBeforeSuite and run for each process.
func BeforeSuiteCredentialSetup(ctx context.Context, filePath string) SuiteConfiguration {
	Expect(filePath).NotTo(BeEmpty(), "filepath should be configured") // Fail the test if the filepath flag is not provided
	config, err := e2e.ReadConfig(filePath)
	Expect(err).NotTo(HaveOccurred(), "should read valid test configuration")

	logger := NewLoggerForTests().Logger
	aws, err := e2e.NewAWSConfig(ctx,
		awsconfig.WithRegion(config.ClusterRegion),
		// We use a custom AppId so the requests show that they were
		// made by the e2e suite in the user-agent
		awsconfig.WithAppID("nodeadm-e2e-test-suite"),
	)
	Expect(err).NotTo(HaveOccurred())

	infra, err := peered.Setup(ctx, logger, aws, config.ClusterName, config.Endpoint)
	Expect(err).NotTo(HaveOccurred(), "should setup e2e resources for peered test")

	skipCleanup := os.Getenv("SKIP_CLEANUP") == "true"

	// DeferCleanup is context aware, so it will behave as SynchronizedAfterSuite
	// We prefer this because it's simpler and it avoids having to share global state
	DeferCleanup(func(ctx context.Context) {
		if skipCleanup {
			logger.Info("Skipping cleanup of e2e resources stack")
			return
		}
		Expect(infra.Teardown(ctx)).To(Succeed(), "should teardown e2e resources")
	}, NodeTimeout(constants.DeferCleanupTimeout))

	return SuiteConfiguration{
		TestConfig:             config,
		SkipCleanup:            skipCleanup,
		CredentialsStackOutput: &infra.Credentials.StackOutput,
		RolesAnywhereCACertPEM: infra.Credentials.RolesAnywhereCA.CertPEM,
		RolesAnywhereCAKeyPEM:  infra.Credentials.RolesAnywhereCA.KeyPEM,
		PublicKey:              infra.NodesPublicSSHKey,
		JumpboxInstanceId:      infra.JumpboxInstanceId,
	}
}

func BeforeSuiteCredentialUnmarshal(ctx context.Context, data []byte) *SuiteConfiguration {
	Expect(data).NotTo(BeEmpty(), "suite config should have provided by first process")
	suiteConfig := &SuiteConfiguration{}
	Expect(yaml.Unmarshal(data, suiteConfig)).To(Succeed(), "should unmarshal suite config coming from first test process successfully")
	Expect(suiteConfig.TestConfig).NotTo(BeNil(), "test configuration should have been set")
	Expect(suiteConfig.CredentialsStackOutput).NotTo(BeNil(), "ec2 stack output should have been set")
	return suiteConfig
}

// BeforeVPCTest is a helper function that builds a PeeredVPCTest and sets up
// the credential providers. It is intended to be used in BeforeEach.
func BeforeVPCTest(ctx context.Context, suite *SuiteConfiguration) *PeeredVPCTest {
	Expect(suite).NotTo(BeNil(), "suite configuration should have been set")
	Expect(suite.TestConfig).NotTo(BeNil(), "test configuration should have been set")
	Expect(suite.CredentialsStackOutput).NotTo(BeNil(), "credentials stack output should have been set")

	var err error
	test, err := BuildPeeredVPCTestForSuite(ctx, suite)
	Expect(err).NotTo(HaveOccurred(), "should build peered VPC test config")

	return test
}

type OSProvider struct {
	OS       e2e.NodeadmOS
	Provider e2e.NodeadmCredentialsProvider
}

func OSProviderList(credentialProviders []e2e.NodeadmCredentialsProvider) []OSProvider {
	osList := []e2e.NodeadmOS{
		// osystem.NewUbuntu2004AMD(),
		// osystem.NewUbuntu2004ARM(),
		// osystem.NewUbuntu2004DockerSource(),
		// osystem.NewUbuntu2204AMD(),
		// osystem.NewUbuntu2204ARM(),
		// osystem.NewUbuntu2204DockerSource(),
		// osystem.NewUbuntu2404AMD(),
		// osystem.NewUbuntu2404ARM(),
		// osystem.NewUbuntu2404DockerSource(),
		// osystem.NewAmazonLinux2023AMD(),
		// osystem.NewAmazonLinux2023ARM(),
		// osystem.NewRedHat8AMD(os.Getenv("RHEL_USERNAME"), os.Getenv("RHEL_PASSWORD")),
		// osystem.NewRedHat8ARM(os.Getenv("RHEL_USERNAME"), os.Getenv("RHEL_PASSWORD")),
		// osystem.NewRedHat9AMD(os.Getenv("RHEL_USERNAME"), os.Getenv("RHEL_PASSWORD")),
		// osystem.NewRedHat9ARM(os.Getenv("RHEL_USERNAME"), os.Getenv("RHEL_PASSWORD")),
		osystem.NewBottleRocket(),
	}
	osProviderList := []OSProvider{}
	for _, nodeOS := range osList {
	providerLoop:
		for _, provider := range credentialProviders {
			if notSupported.Matches(nodeOS.Name(), provider.Name()) {
				continue providerLoop
			}
			osProviderList = append(osProviderList, OSProvider{OS: nodeOS, Provider: provider})
		}
	}
	return osProviderList
}

func CredentialProviders() []e2e.NodeadmCredentialsProvider {
	return []e2e.NodeadmCredentialsProvider{
		&credentials.SsmProvider{},
		&credentials.IamRolesAnywhereProvider{},
	}
}

func AddClientsToCredentialProviders(credentialProviders []e2e.NodeadmCredentialsProvider, test *PeeredVPCTest) []e2e.NodeadmCredentialsProvider {
	result := []e2e.NodeadmCredentialsProvider{}
	for _, provider := range credentialProviders {
		switch p := provider.(type) {
		case *credentials.SsmProvider:
			p.SSM = test.SSMClient
			p.Role = test.StackOut.SSMNodeRoleName
		case *credentials.IamRolesAnywhereProvider:
			p.RoleARN = test.StackOut.IRANodeRoleARN
			p.ProfileARN = test.StackOut.IRAProfileARN
			p.TrustAnchorARN = test.StackOut.IRATrustAnchorARN
			p.CA = test.RolesAnywhereCA
		}
		result = append(result, provider)
	}
	return result
}

type NodeCreate struct {
	InstanceName string
	InstanceSize e2e.InstanceSize
	NodeName     string
	OS           e2e.NodeadmOS
	Provider     e2e.NodeadmCredentialsProvider
}

func CreateNodes(ctx context.Context, test *PeeredVPCTest, nodesToCreate []NodeCreate) {
	var wg sync.WaitGroup
	for _, entry := range nodesToCreate {
		wg.Add(1)
		go func(entry NodeCreate) {
			defer wg.Done()
			defer GinkgoRecover()
			testNode := test.NewTestNode(ctx, entry.InstanceName, entry.NodeName, test.Cluster.KubernetesVersion, entry.OS, entry.Provider, entry.InstanceSize)
			Expect(testNode.Start(ctx)).To(Succeed(), "node should start successfully")
			Expect(testNode.Verify(ctx)).To(Succeed(), "node should be fully functional")
		}(entry)
	}
	wg.Wait()
}
