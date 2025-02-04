package peered

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"

	"github.com/aws/eks-hybrid/test/e2e"
	"github.com/aws/eks-hybrid/test/e2e/ec2"
	"github.com/aws/eks-hybrid/test/e2e/ssh"
)

// SerialOutputBlock is a helper to run sections of a ginkgo test while outputting the serial console output of an instance.
// It paused the main test logs while streaming the serial console output, and resumes them once the test "body" is done.
// The serial console output is also saved to a file until Close is called, no matter if you are running a test block or not.
// This is very useful to help debugging issues with the node joining the cluster, specially if the process runs as part of the node initialization.
type SerialOutputBlock struct {
	serial       *ssh.SerialConsole
	logsFile     io.WriteCloser
	serialOutout *e2e.SwitchWriter
	testLogger   e2e.PausableLogger
}

func NewSerialOutputBlock(ctx context.Context, peeredNode *Node, instance ec2.Instance, testLogger e2e.PausableLogger) (*SerialOutputBlock, error) {
	serial, err := peeredNode.SerialConsole(ctx, instance.ID)
	if err != nil {
		return nil, fmt.Errorf("preparing EC2 for serial connection: %w", err)
	}

	pausableOutput := e2e.NewSwitchWriter(os.Stdout)
	pausableOutput.Pause() // We start it paused, we will resume it once the test output is paused
	file, err := os.Create(instance.Name + "-serial-output.log")
	if err != nil {
		return nil, fmt.Errorf("creating file to store serial output: %w", err)
	}

	if err := serial.Copy(io.MultiWriter(pausableOutput, file)); err != nil {
		return nil, fmt.Errorf("connecting to EC2 serial console: %w", err)
	}

	return &SerialOutputBlock{
		serial:       serial,
		logsFile:     file,
		testLogger:   testLogger,
		serialOutout: pausableOutput,
	}, nil
}

// It runs the test body while streaming the serial console output of the instance to stdout.
// It pauses the main test logs while streaming the serial console output, and resumes them once the test "body" is done.
// This actually doesn't create a ginkgo node, it just uses By to print the description and help distinguish this
// test block in the logs.
func (b *SerialOutputBlock) It(description string, body func()) {
	// This ensures that test logs are restored even if this method panics
	// Both Paused and Resume are idempotent
	defer func() {
		b.serialOutout.Pause()
		gomega.Expect(b.testLogger.Resume()).To(gomega.Succeed())
	}()

	ginkgo.By(description, func() {
		b.testLogger.Info("Streaming Node serial output while waiting for node. Test logs are paused in the meantime and will resume later.")
		b.testLogger.Pause()
		// hack: this prints the resume message immediately after resuming the test logs
		// and more importantly, before any logs produced by body()
		b.testLogger.Info("Node serial output stopped and test logs resumed")
		fmt.Println("-------------- Serial output starts here --------------")
		gomega.Expect(b.serialOutout.Resume()).To(gomega.Succeed())
		body()
		b.serialOutout.Pause()
		fmt.Println("-------------- Serial output ends here --------------")
		gomega.Expect(b.testLogger.Resume()).To(gomega.Succeed())
	})
}

func (b *SerialOutputBlock) Close() {
	b.serial.Close()
	b.logsFile.Close()
}
