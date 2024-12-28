package kubernetes

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/version"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/scheme"
	restclient "k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"k8s.io/kubectl/pkg/drain"

	utilerrors "k8s.io/apimachinery/pkg/util/errors"
)

const (
	nodePodWaitTimeout       = 3 * time.Minute
	nodePodDelayInterval     = 5 * time.Second
	hybridNodeWaitTimeout    = 10 * time.Minute
	hybridNodeDelayInterval  = 5 * time.Second
	hybridNodeUpgradeTimeout = 2 * time.Minute
	MinimumVersion           = "1.25"
)

// WaitForNode wait for the node to join the cluster and fetches the node info from an internal IP address of the node
func WaitForNode(ctx context.Context, k8s *kubernetes.Clientset, internalIP string, logger logr.Logger) (*corev1.Node, error) {
	foundNode := &corev1.Node{}
	consecutiveErrors := 0
	err := wait.PollUntilContextTimeout(ctx, hybridNodeDelayInterval, hybridNodeWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		node, err := getNodeByInternalIP(ctx, k8s, internalIP)
		if err != nil {
			consecutiveErrors += 1
			if consecutiveErrors > 3 {
				return false, err
			}
			logger.Info("Retryable error listing nodes when looking for node with IP. Continuing to poll", "internalIP", internalIP, "error", err)
			return false, nil // continue polling
		}
		consecutiveErrors = 0
		if node != nil {
			foundNode = node
			return true, nil // node found, stop polling
		}

		logger.Info("Node with internal IP doesn't exist yet", "internalIP", internalIP)
		return false, nil // continue polling
	})
	if err != nil {
		return nil, err
	}
	return foundNode, nil
}

func getNodeByInternalIP(ctx context.Context, k8s *kubernetes.Clientset, internalIP string) (*corev1.Node, error) {
	nodes, err := k8s.CoreV1().Nodes().List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("listing nodes when looking for node with IP %s: %w", internalIP, err)
	}
	return nodeByInternalIP(nodes, internalIP), nil
}

func nodeByInternalIP(nodes *corev1.NodeList, nodeIP string) *corev1.Node {
	for _, node := range nodes.Items {
		for _, address := range node.Status.Addresses {
			if address.Type == "InternalIP" && address.Address == nodeIP {
				return &node
			}
		}
	}
	return nil
}

func WaitForHybridNodeToBeReady(ctx context.Context, k8s *kubernetes.Clientset, nodeName string, logger logr.Logger) error {
	consecutiveErrors := 0
	err := wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, hybridNodeWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		node, err := k8s.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			logger.Info("Node does not exist yet", "node", nodeName)
			return false, nil
		}
		if err != nil {
			consecutiveErrors += 1
			if consecutiveErrors > 3 {
				return false, fmt.Errorf("getting hybrid node %s: %w", nodeName, err)
			}
			logger.Info("Retryable error getting hybrid node. Continuing to poll", "name", nodeName, "error", err)
			return false, nil // continue polling
		}
		consecutiveErrors = 0

		if nodeReady(node) {
			logger.Info("Node is ready", "node", nodeName)
			return true, nil // node is ready, stop polling
		} else {
			logger.Info("Node is not ready yet", "node", nodeName)
		}

		return false, nil // continue polling
	})
	if err != nil {
		return fmt.Errorf("waiting for node %s to be ready: %w", nodeName, err)
	}

	return nil
}

func WaitForHybridNodeToBeNotReady(ctx context.Context, k8s *kubernetes.Clientset, nodeName string, logger logr.Logger) error {
	consecutiveErrors := 0
	err := wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, hybridNodeWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		node, err := k8s.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			consecutiveErrors += 1
			if consecutiveErrors > 3 {
				return false, fmt.Errorf("getting hybrid node %s: %w", nodeName, err)
			}
			logger.Info("Retryable error getting hybrid node. Continuing to poll", "name", nodeName, "error", err)
			return false, nil // continue polling
		}
		consecutiveErrors = 0

		if !nodeReady(node) {
			logger.Info("Node is not ready", "node", nodeName)
			return true, nil // node is not ready, stop polling
		} else {
			logger.Info("Node is still ready", "node", nodeName)
		}

		return false, nil // continue polling
	})
	if err != nil {
		return fmt.Errorf("waiting for node %s to be not ready: %w", nodeName, err)
	}

	return nil
}

func nodeReady(node *corev1.Node) bool {
	for _, cond := range node.Status.Conditions {
		if cond.Type == corev1.NodeReady {
			return cond.Status == corev1.ConditionTrue
		}
	}
	return false
}

func GetNginxPodName(name string) string {
	return "nginx-" + name
}

func CreateNginxPodInNode(ctx context.Context, k8s *kubernetes.Clientset, nodeName, namespace string, logger logr.Logger) error {
	podName := GetNginxPodName(nodeName)
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      podName,
			Namespace: namespace,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "nginx",
					Image: "public.ecr.aws/nginx/nginx:latest",
					Ports: []corev1.ContainerPort{
						{
							ContainerPort: 80,
						},
					},
				},
			},
			// schedule the pod on the specific node using nodeSelector
			NodeSelector: map[string]string{
				"kubernetes.io/hostname": nodeName,
			},
			RestartPolicy: corev1.RestartPolicyNever,
		},
	}

	_, err := k8s.CoreV1().Pods(namespace).Create(ctx, pod, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("creating the test pod: %w", err)
	}

	err = waitForPodToBeRunning(ctx, k8s, podName, namespace, nodeName, logger)
	if err != nil {
		logger.Info("Getting events from nginx pod")
		events, _ := k8s.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{FieldSelector: "involvedObject.name=" + podName, TypeMeta: metav1.TypeMeta{Kind: "Pod"}})
		for _, item := range events.Items {
			logger.Info(fmt.Sprintf("%s : %s", item.Reason, item.Message))
		}
		logger.Info("Getting events kube system pods")
		events, _ = k8s.CoreV1().Events("kube-system").List(ctx, metav1.ListOptions{TypeMeta: metav1.TypeMeta{Kind: "Pod"}})
		for _, item := range events.Items {
			logger.Info(fmt.Sprintf("%s : %s : %s", item.ObjectMeta.Name, item.Reason, item.Message))
		}
		logger.Info("Getting logs from cilium-operator")
		pods, _ := k8s.CoreV1().Pods("kube-system").List(ctx, metav1.ListOptions{LabelSelector: "app.kubernetes.io/name=cilium-operator", TypeMeta: metav1.TypeMeta{Kind: "Pod"}})
		for _, pod := range pods.Items {
			req := k8s.CoreV1().Pods(namespace).GetLogs(pod.ObjectMeta.Name, &corev1.PodLogOptions{Previous: true})
			podLogs, err := req.Stream(ctx)
			if err != nil {
				return fmt.Errorf("opening log stream: %w", err)
			}
			defer podLogs.Close()

			buf := new(bytes.Buffer)
			if _, err = io.Copy(buf, podLogs); err != nil {
				return fmt.Errorf("getting logs from stream: %w", err)
			}
			logger.Info(buf.String())
		}
		return fmt.Errorf("waiting for test pod to be running: %w", err)
	}
	return nil
}

func waitForPodToBeRunning(ctx context.Context, k8s *kubernetes.Clientset, name, namespace, nodeName string, logger logr.Logger) error {
	consecutiveErrors := 0
	return wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, nodePodWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		pod, err := k8s.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})
		if err != nil {
			consecutiveErrors += 1
			if consecutiveErrors > 3 {
				return false, fmt.Errorf("getting test pod: %w", err)
			}
			logger.Info("Retryable error getting test pod. Continuing to poll", "name", name, "error", err)
			return false, nil // continue polling
		}
		consecutiveErrors = 0

		if pod.Status.Phase == corev1.PodRunning {
			return true, nil // pod is running, stop polling
		}
		return false, nil // continue polling
	})
}

func waitForPodToBeDeleted(ctx context.Context, k8s *kubernetes.Clientset, name, namespace string) error {
	return wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, nodePodWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		_, err = k8s.CoreV1().Pods(namespace).Get(ctx, name, metav1.GetOptions{})

		if errors.IsNotFound(err) {
			return true, nil
		} else if err != nil {
			return false, err
		}

		return false, nil
	})
}

func DeletePod(ctx context.Context, k8s *kubernetes.Clientset, name, namespace string) error {
	err := k8s.CoreV1().Pods(namespace).Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting pod: %w", err)
	}
	return waitForPodToBeDeleted(ctx, k8s, name, namespace)
}

func DeleteNode(ctx context.Context, k8s *kubernetes.Clientset, name string) error {
	err := k8s.CoreV1().Nodes().Delete(ctx, name, metav1.DeleteOptions{})
	if err != nil {
		return fmt.Errorf("deleting node: %w", err)
	}
	return nil
}

func EnsureNodeWithIPIsDeleted(ctx context.Context, k8s *kubernetes.Clientset, internalIP string) error {
	node, err := getNodeByInternalIP(ctx, k8s, internalIP)
	if err != nil {
		return fmt.Errorf("getting node by internal IP: %w", err)
	}
	if node == nil {
		return nil
	}

	err = DeleteNode(ctx, k8s, node.Name)
	if err != nil {
		return fmt.Errorf("deleting node %s: %w", node.Name, err)
	}
	return nil
}

func WaitForNodeToHaveVersion(ctx context.Context, k8s *kubernetes.Clientset, nodeName, targetVersion string, logger logr.Logger) (*corev1.Node, error) {
	foundNode := &corev1.Node{}
	consecutiveErrors := 0
	err := wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, hybridNodeUpgradeTimeout, true, func(ctx context.Context) (done bool, err error) {
		node, err := k8s.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{})
		if err != nil {
			consecutiveErrors += 1
			logger.Info("consecutiveErrors", "consecutiveErrors", consecutiveErrors)
			if consecutiveErrors > 3 {
				return false, fmt.Errorf("getting hybrid node %s: %w", nodeName, err)
			}
			logger.Info("Retryable error getting hybrid node. Continuing to poll", "name", nodeName, "error", err)
			return false, nil // continue polling
		}
		consecutiveErrors = 0

		kubernetesVersion := strings.TrimPrefix(node.Status.NodeInfo.KubeletVersion, "v")
		// If the current version matches the target version of kubelet, return true to stop polling
		if strings.HasPrefix(kubernetesVersion, targetVersion) {
			foundNode = node
			logger.Info("Node successfully upgraded to desired kubernetes version", "version", targetVersion)
			return true, nil
		}

		return false, nil // continue polling
	})
	if err != nil {
		return nil, fmt.Errorf("waiting for node %s kubernetes version to be upgraded to %s: %w", nodeName, targetVersion, err)
	}

	return foundNode, nil
}

func PreviousVersion(kubernetesVersion string) (string, error) {
	currentVersion, err := version.ParseSemantic(kubernetesVersion + ".0")
	if err != nil {
		return "", fmt.Errorf("parsing version: %v", err)
	}
	prevVersion := fmt.Sprintf("%d.%d", currentVersion.Major(), currentVersion.Minor()-1)
	return prevVersion, nil
}

func IsPreviousVersionSupported(kubernetesVersion string) (bool, error) {
	prevVersion, err := PreviousVersion(kubernetesVersion)
	if err != nil {
		return false, err
	}
	minVersion := version.MustParseSemantic(MinimumVersion + ".0")
	return version.MustParseSemantic(prevVersion + ".0").AtLeast(minVersion), nil
}

func DrainNode(ctx context.Context, k8s *kubernetes.Clientset, node *corev1.Node) error {
	helper := &drain.Helper{
		Ctx:                             ctx,
		Client:                          k8s,
		Force:                           true, // Force eviction
		GracePeriodSeconds:              -1,   // Use pod's default grace period
		IgnoreAllDaemonSets:             true, // Ignore DaemonSet-managed pods
		DisableEviction:                 true, // forces drain to use delete rather than evict
		DeleteEmptyDirData:              true,
		SkipWaitForDeleteTimeoutSeconds: 0,
		Out:                             os.Stdout,
		ErrOut:                          os.Stderr,
	}

	err := CordonNode(ctx, k8s, node)
	if err != nil {
		return err
	}
	for {
		err = drain.RunNodeDrain(helper, node.Name)
		if err != nil {
			return fmt.Errorf("draining node %s: %v", node.Name, err)
		}

		list, errs := helper.GetPodsForDeletion(node.Name)
		if errs != nil {
			return utilerrors.NewAggregate(errs)
		}
		if warnings := list.Warnings(); warnings != "" {
			fmt.Fprintf(helper.ErrOut, "WARNING: %s\n", warnings)
		}
		if len(list.Pods()) == 0 {
			return nil
		}
		for _, pod := range list.Pods() {
			fmt.Fprintf(helper.ErrOut, "Pod still running after drain: %s\n", pod.ObjectMeta.Name)
		}
	}
}

func UncordonNode(ctx context.Context, k8s *kubernetes.Clientset, node *corev1.Node) error {
	helper := &drain.Helper{
		Ctx:    ctx,
		Client: k8s,
	}

	err := drain.RunCordonOrUncordon(helper, node, false)
	if err != nil {
		return fmt.Errorf("cordoning node %s: %v", node.Name, err)
	}

	return nil
}

func CordonNode(ctx context.Context, k8s *kubernetes.Clientset, node *corev1.Node) error {
	helper := &drain.Helper{
		Ctx:    ctx,
		Client: k8s,
	}

	err := drain.RunCordonOrUncordon(helper, node, true)
	if err != nil {
		return fmt.Errorf("cordoning node %s: %v", node.Name, err)
	}

	return nil
}

func GetPodLogs(ctx context.Context, k8s *kubernetes.Clientset, name, namespace string) (string, error) {
	req := k8s.CoreV1().Pods(namespace).GetLogs(name, &corev1.PodLogOptions{})
	podLogs, err := req.Stream(ctx)
	if err != nil {
		return "", fmt.Errorf("opening log stream: %w", err)
	}
	defer podLogs.Close()

	buf := new(bytes.Buffer)
	if _, err = io.Copy(buf, podLogs); err != nil {
		return "", fmt.Errorf("getting logs from stream: %w", err)
	}

	return buf.String(), nil
}

func ExecPod(ctx context.Context, config *restclient.Config, k8s *kubernetes.Clientset, name, namespace string, cmd ...string) (stdout, stderr string, err error) {
	req := k8s.CoreV1().RESTClient().Post().Resource("pods").Name(name).Namespace(namespace).SubResource("exec")
	req.VersionedParams(
		&v1.PodExecOptions{
			Command: cmd,
			Stdin:   false,
			Stdout:  true,
			Stderr:  true,
			TTY:     true,
		},
		scheme.ParameterCodec,
	)
	exec, err := remotecommand.NewSPDYExecutor(config, "POST", req.URL())
	if err != nil {
		return "", "", err
	}
	var stdoutBuf, stderrBuf bytes.Buffer
	err = exec.StreamWithContext(ctx, remotecommand.StreamOptions{
		Stdin:  nil,
		Stdout: &stdoutBuf,
		Stderr: &stderrBuf,
	})
	if err != nil {
		return "", "", err
	}

	return stdoutBuf.String(), stderrBuf.String(), nil
}
