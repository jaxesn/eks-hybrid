# Hydra Failure Analysis Tool

This tool provides automated analysis of Hydra test failures in AWS environments.

## Usage Instructions

1. Load the protocol file:
   ```
   load hydra_failure_analysis_protocol.md
   ```

2. Trigger the analysis with your failure summary:
   ```
   analyze hydra failure: [paste your failure summary here]
   ```

3. The tool will automatically:
   - Parse the failure details
   - Identify and suggest the appropriate AWS account and role
   - **Ask for your confirmation of the AWS profile/role** (only confirmation needed)
   - Execute all necessary AWS CLI commands
   - Analyze logs and CloudTrail events
   - Provide a comprehensive failure analysis report

Minimal user interaction is required - you'll only need to confirm the AWS profile selection.

## Example

```
analyze hydra failure: The prod canary in account <id>> has failed in region us-west-2
Task logs
Cluster logs
Job duration: 0 hours 23 minutes 56 seconds
Tests Ran: 2/68
Tests Failed: 1/2
Nodeadm Version: v1.0.7
Failure Message:
Phase: ExecuteTests Failure: nodeadm e2e test ginkgo command failed: exit status 1 Status: failure
...
```

## Requirements

- AWS CLI configured with appropriate profiles
- Permissions to access CloudTrail, S3, and CloudWatch logs
- Access to the AWS account where the failure occurred
