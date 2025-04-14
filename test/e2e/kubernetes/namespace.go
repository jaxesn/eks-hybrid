package kubernetes

import (
	"context"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

func WaitForNamespaceToBeDeleted(ctx context.Context, k8s kubernetes.Interface, name string) error {
	return wait.PollUntilContextTimeout(ctx, nodePodDelayInterval, nodePodWaitTimeout, true, func(ctx context.Context) (done bool, err error) {
		_, err = k8s.CoreV1().Namespaces().Get(ctx, name, metav1.GetOptions{})

		if apierrors.IsNotFound(err) {
			return true, nil
		} else if err != nil {
			return false, err
		}

		return false, nil
	})
}
