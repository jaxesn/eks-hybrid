package run

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	ginkgoTypes "github.com/onsi/ginkgo/v2/types"

	"github.com/aws/eks-hybrid/test/e2e/constants"
)

type TestReportEntry struct {
	ArtifactsPath string
	TestName      string
	Instances     []InstanceReportEntry
}

type InstanceReportEntry struct {
	InstanceName        string
	LogBundleFile       string
	SerialOutputLogFile string
}

type E2EReport struct {
	ArtifactsFolder string
}

func (e *E2EReport) Parse(ctx context.Context, reportPath string) (E2EResult, error) {
	_, err := os.Stat(reportPath)
	if err != nil && errors.Is(err, os.ErrNotExist) {
		return E2EResult{}, nil
	}
	if err != nil {
		return E2EResult{}, fmt.Errorf("report file not found: %w", err)
	}

	e2eResult, err := e.parseJSONReport(reportPath)
	if err != nil {
		return e2eResult, fmt.Errorf("parsing ginkgo json report: %w", err)
	}

	return e2eResult, nil
}

func (e *E2EReport) parseJSONReport(reportPath string) (E2EResult, error) {
	e2eResult := E2EResult{}
	reportData, err := os.ReadFile(reportPath)
	if err != nil {
		return e2eResult, fmt.Errorf("reading report: %w", err)
	}

	var reports []ginkgoTypes.Report
	err = json.Unmarshal(reportData, &reports)
	if err != nil {
		return e2eResult, fmt.Errorf("unmarshalling report: %w", err)
	}

	if len(reports) > 1 {
		return e2eResult, fmt.Errorf("multiple reports found, expected 1")
	}
	report := reports[0]

	e2eResult.TotalTests = report.PreRunStats.TotalSpecs

	// Process each failed test
	reportErrors := []error{}
	for _, spec := range report.SpecReports {
		if spec.State == ginkgoTypes.SpecStateSkipped {
			continue
		}

		if spec.LeafNodeType == ginkgoTypes.NodeTypeIt {
			e2eResult.TestRan = e2eResult.TestRan + 1
		}

		// s3 artifacts path is set on the spec, try to find it to set at
		// the root e2eresult level
		testReportEntry, err := getTestReportEntry(spec)
		if err != nil {
			reportErrors = append(reportErrors, fmt.Errorf("getting test report entry: %w", err))
		}

		artifactsPath := testReportEntry.ArtifactsPath
		if artifactsPath != "" && e2eResult.ArtifactsBucketPath == "" {
			// strip test name from path, which is the last part of the path
			// ex: ?prefix=logs/<cluster-name>/<instance-name>/
			path := strings.TrimSuffix(artifactsPath, "/")
			e2eResult.ArtifactsBucketPath = path[:strings.LastIndex(path, "/")+1]
		}

		if spec.State == ginkgoTypes.SpecStatePassed {
			continue
		}

		// This can be one of BeforeSuite, DeferCleanup and It
		// if it failed, it will be included
		// only Its will have log files created since ginkgo only captures stdout/stderr for It
		leafType := spec.LeafNodeType
		name := leafType.String()

		failedTest := FailedTest{
			Name:           name,
			State:          spec.State.String(),
			FailureMessage: specFailureMessage(spec),
		}

		if artifactsPath != "" {
			failedTest.GinkgoLog = artifactsPath + testGinkgoOutputLog
		}

		// TODO: current test only add one instance
		// conformance will add multiple instances
		if len(testReportEntry.Instances) > 0 {
			failedTest.CollectorLogsBundle = testReportEntry.Instances[0].LogBundleFile
			failedTest.SerialLog = testReportEntry.Instances[0].SerialOutputLogFile
			failedTest.Name = testReportEntry.Instances[0].InstanceName
		}

		e2eResult.FailedTests = append(e2eResult.FailedTests, failedTest)

		// Only process "It" test nodes for detailed logs
		if spec.LeafNodeType != ginkgoTypes.NodeTypeIt {
			continue
		}

		e2eResult.TestFailed = e2eResult.TestFailed + 1

		if saveErr := e.saveTestLogFiles(spec, failedTest.Name); saveErr != nil {
			reportErrors = append(reportErrors, fmt.Errorf("saving log file: %w", saveErr))
		}
	}

	if e2eResult.ArtifactsBucketPath != "" {
		e2eResult.GinkgoLog = e2eResult.ArtifactsBucketPath + testGinkgoOutputLog
		e2eResult.SetupLog = e2eResult.ArtifactsBucketPath + testSetupLogFile
		e2eResult.CleanupLog = e2eResult.ArtifactsBucketPath + testCleanupLogFile
	}

	return e2eResult, errors.Join(reportErrors...)
}

// saveTestLogFile creates a detailed log file for a test and uploads it to S3 if configured
// Returns the S3 path where the log was uploaded or an error
func (e *E2EReport) saveTestLogFiles(spec ginkgoTypes.SpecReport, specName string) error {
	logsDir := filepath.Join(e.ArtifactsFolder, specName)
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return fmt.Errorf("creating test logs directory: %w", err)
	}

	logFilePath := filepath.Join(logsDir, testGinkgoOutputLog)
	sb := strings.Builder{}

	sb.WriteString(fmt.Sprintf("Test: [%s]\n", specName))
	sb.WriteString(fmt.Sprintf("State: %s\n", spec.State.String()))
	sb.WriteString(fmt.Sprintf("Duration: %.3f seconds\n", spec.RunTime.Seconds()))
	sb.WriteString(fmt.Sprintf("Start Time: %s\n", spec.StartTime.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("End Time: %s\n\n", spec.EndTime.Format(time.RFC3339)))

	if spec.CapturedStdOutErr != "" {
		sb.WriteString("Captured StdOut/StdErr Output >>\n")
		sb.WriteString(spec.CapturedStdOutErr)
		sb.WriteString("\n\n")
	}

	sb.WriteString(specFailureMessage(spec))

	if err := os.WriteFile(logFilePath, []byte(sb.String()), 0o644); err != nil {
		return fmt.Errorf("writing test log file: %w", err)
	}

	return nil
}

func specFailureMessage(spec ginkgoTypes.SpecReport) string {
	if spec.Failure.Message == "" {
		return ""
	}
	sb := strings.Builder{}
	sb.WriteString("Failure Details >>\n")
	sb.WriteString(fmt.Sprintf("[FAILED] %s\n", spec.LeafNodeText))
	sb.WriteString(fmt.Sprintf("\tExpected %s\n", strings.ReplaceAll(spec.Failure.Message, "\n", "\n\t\t")))
	if spec.Failure.Location.FileName != "" {
		timestamp := spec.Failure.TimelineLocation.Time.Format(time.DateTime)
		sb.WriteString(fmt.Sprintf("\tIn [%s] at: %s:%d @ %s\n",
			spec.LeafNodeType.String(),
			spec.Failure.Location.FileName,
			spec.Failure.Location.LineNumber,
			timestamp))
	}
	sb.WriteString("\n")
	return sb.String()
}

func getTestReportEntry(spec ginkgoTypes.SpecReport) (TestReportEntry, error) {
	var testReportEntryEntry ginkgoTypes.ReportEntry
	for _, entry := range spec.ReportEntries {
		if entry.Name == constants.TestReportEntry {
			testReportEntryEntry = entry
		}
	}
	if testReportEntryEntry.Name == "" {
		return TestReportEntry{}, nil
	}

	var testReportEntry TestReportEntry
	err := json.Unmarshal([]byte(testReportEntryEntry.Value.AsJSON), &testReportEntry)
	if err != nil {
		return TestReportEntry{}, fmt.Errorf("unmarshalling test report entry: %w", err)
	}

	return testReportEntry, nil
}
