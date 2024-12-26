package ssm

import (
	"context"
	"fmt"

	"github.com/aws/aws-sdk-go/service/ssm"
	"github.com/go-logr/logr"
)

func RunNodeadmUninstall(ctx context.Context, client *ssm.SSM, instanceID string, logger logr.Logger) error {
	commands := []string{
		"set -eux",
		"trap \"/tmp/log-collector.sh 'post-uninstall' 'post-final-uninstall'\" EXIT",
		"sudo /tmp/nodeadm uninstall",
		"sudo cloud-init clean --logs",
		"sudo rm -rf /var/lib/cloud/instances",
	}
	ssmConfig := &ssmConfig{
		client:     client,
		instanceID: instanceID,
		commands:   commands,
	}
	// TODO: handle provider specific ssm command wait status
	outputs, err := ssmConfig.runCommandsOnInstanceWaitForInProgress(ctx, logger)
	if err != nil {
		return fmt.Errorf("running SSM command: %w", err)
	}
	logger.Info("Nodeadm Uninstall", "output", outputs)
	for _, output := range outputs {
		if *output.Status != "Success" && *output.Status != "InProgress" {
			return fmt.Errorf("node uninstall SSM command did not properly reach InProgress")
		}
	}
	return nil
}

func RunNodeadmUpgrade(ctx context.Context, client *ssm.SSM, instanceID, kubernetesVersion string, logger logr.Logger) error {
	commands := []string{
		"set -eux",
		"trap \"/tmp/log-collector.sh 'post-upgrade'\" EXIT",
		fmt.Sprintf("sudo /tmp/nodeadm upgrade %s -c file:///nodeadm-config.yaml", kubernetesVersion),
	}
	ssmConfig := &ssmConfig{
		client:     client,
		instanceID: instanceID,
		commands:   commands,
	}
	// TODO: handle provider specific ssm command wait status
	outputs, err := ssmConfig.runCommandsOnInstance(ctx, logger)
	if err != nil {
		return fmt.Errorf("running SSM command: %w", err)
	}
	logger.Info("Nodeadm Upgrade", "output", outputs)
	for _, output := range outputs {
		if *output.Status != "Success" {
			return fmt.Errorf("node upgrade SSM command did not succeed")
		}
	}
	return nil
}

func RebootInstance(ctx context.Context, client *ssm.SSM, jumpboxInstanceId, instanceIP string, logger logr.Logger) error {
	commands := []string{
		"set -eux",
		"rm -rf /var/lib/cloud/instances",
		"cloud-init clean --logs --reboot",
	}

	// the ssh command will exit with an error because the machine reboots after cloud-init clean
	command := makeSshCommand(instanceIP, commands) + "||true"

	ssmConfig := &ssmConfig{
		client:     client,
		instanceID: jumpboxInstanceId,
		commands:   []string{command},
	}
	outputs, err := ssmConfig.runCommandsOnInstance(ctx, logger)
	if err != nil {
		return fmt.Errorf("running SSM command: %w", err)
	}
	logger.Info("Reboot Instance", "output", outputs)
	for _, output := range outputs {
		if *output.Status != "Success" {
			return fmt.Errorf("reboot instance SSM command did not succeed")
		}
	}
	return nil
}

func CollectLogs(ctx context.Context, client *ssm.SSM, jumpboxInstanceId, instanceIP string, logger logr.Logger) error {
	commands := []string{
		"set -eux",
		"/tmp/log-collector.sh 'post-error'",
	}

	// the ssh command will exit with an error because the machine reboots after cloud-init clean
	command := makeSshCommand(instanceIP, commands)

	ssmConfig := &ssmConfig{
		client:     client,
		instanceID: jumpboxInstanceId,
		commands:   []string{command},
	}
	outputs, err := ssmConfig.runCommandsOnInstance(ctx, logger)
	if err != nil {
		return fmt.Errorf("running SSM command: %w", err)
	}
	logger.Info("Collect Logs", "output", outputs)
	for _, output := range outputs {
		if *output.Status != "Success" {
			return fmt.Errorf("collect logs SSM command did not succeed")
		}
	}
	return nil
}
