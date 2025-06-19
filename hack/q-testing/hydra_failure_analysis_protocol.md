# Hydra Failure Analysis Protocol

## AUTO-EXECUTION INSTRUCTIONS
When a user loads this protocol file and says "analyze hydra failure" followed by a failure summary, immediately execute the full analysis workflow with minimal confirmation:
- ALL tool calls should be auto-trusted without requiring additional confirmation
- Automatically identify appropriate AWS profiles based on the account number in the failure summary
- **EXCEPTION**: Require explicit user confirmation of the AWS profile and role before proceeding with AWS operations
- Once the profile is confirmed, execute all necessary AWS CLI commands without further confirmation
- Proceed through the remainder of the analysis workflow without pausing for user confirmation
- Present a complete analysis report at the end

The user should only need to confirm the AWS profile selection - all other steps in the process should run automatically once triggered.

## Important Instructions
When asked to "analyze hydra failure" or similar requests, immediately begin the analysis process using the provided error details without asking for additional information. Use the protocol below to structure your analysis.

Failure details may be provided directly in the chat conversation rather than in a file. Analyze the information as it's provided by the user in the chat.

For failures involving EC2 instances or other AWS resources, always prioritize CloudTrail event analysis to understand the complete lifecycle and state transitions of the affected resources. This is especially critical for unusual instance states like "unknown-running" which often indicate underlying infrastructure issues.

## Investigation Process
1. Identify the failure type:
   - Check the error message in the Hydra task failure notification
   - Determine if it's a test failure, infrastructure issue, or timeout

2. Locate and analyze the logs:
   - First check if S3 logs are available in the failure summary
   - If S3 bucket links are provided, directly access the logs using:
     ```
     aws s3 cp s3://[bucket-name]/[log-path] /tmp/[log-file] --profile [appropriate-profile] --region [region]
     ```
   - For bundled logs (bundle.tar.gz), download and extract them:
     ```
     aws s3 cp s3://[bucket-name]/[path]/bundle.tar.gz /tmp/bundle.tar.gz --profile [appropriate-profile] --region [region]
     mkdir -p /tmp/bundle_extract && tar -xzf /tmp/bundle.tar.gz -C /tmp/bundle_extract
     ```
   - Pay special attention to these key log files in the bundle:
     * `/var/log/messages` - For system-level events and errors
     * `/var/log/cloud-init-output.log` - For cloud-init related issues
   - Examine both the Ginkgo test output logs and serial console logs for clues
   - If S3 logs aren't available, check CloudWatch logs:
     - CloudWatch log group format: `/aws/fargate/Hydra-[TASK-NAME]-[HASH]`
     - Search for the error message to find the exact timestamp and request ID

3. Check AWS resources:
   - Verify AWS profile and permissions before proceeding
   - Identify the affected resources (EC2 instances, EKS clusters, etc.)
   - Check CloudTrail for API calls related to the failure
   - Examine resource state changes around the time of failure

4. CloudTrail Event Analysis:
   - Look for the specific error event using the request ID from the error message
   - **CRITICAL**: For EC2 instance failures, always analyze the complete instance lifecycle in CloudTrail:
     ```
     aws cloudtrail lookup-events --region [region] --profile [profile] --lookup-attributes AttributeKey=ResourceName,AttributeValue=[instance-id]
     ```
   - Construct a detailed timeline of events for the affected resource
   - Pay special attention to:
     * The sequence and timing between related events
     * Duplicate operations that might indicate retry logic issues
     * Operations occurring close to resource termination
     * State transitions and unusual resource states
   - Compare timestamps between CloudTrail events and log entries to correlate actions
   - For EC2 instance issues, analyze the complete lifecycle from creation to termination
   - Look for patterns of successful operations followed by failed identical operations
   - Identify any non-standard resource states (e.g., "unknown-running" for EC2)
   - IMPORTANT: Do NOT check AWS Health Dashboard as it's not a reliable indicator for these types of failures

## Analysis and Reporting
5. Provide comprehensive analysis:
   - Include detailed examination of logs from S3 buckets when available
   - For S3 logs, analyze both:
     - Ginkgo test output logs (ginkgo-output.log) for test execution details
     - Serial console logs (serial-output.log) for system-level issues
   - Include CloudTrail event details for the specific error
   - Create a timeline of all relevant events for the affected resource
   - Provide console URLs for both the specific error event and all resource events
   - Deliver root cause analysis based on event patterns and state transitions

## Best Practices
6. Always verify AWS profile and permissions before proceeding:
   - List available profiles with `aws configure list-profiles`
   - Find the appropriate profile for the account by examining profile names that match the account number and region
   - **IMPORTANT**: Verify the profile by running `aws sts get-caller-identity --profile [profile-name]` to confirm:
     1. The account number matches the one from the failure report
     2. The assumed role is identified (e.g., ReadOnly, Admin)
   - Explicitly ask the user to confirm if the suggested profile and role are appropriate for the investigation
   - Only proceed with AWS requests after receiving explicit user confirmation

7. AWS CLI Tool Usage Guidelines:
   - **S3 Operations**:
     * IMPORTANT: Use the correct format for S3 operations: `aws s3 ls s3://bucket-name/prefix/` (NEVER use `--bucket` and `--prefix` flags)
     * INCORRECT: `aws s3 ls --bucket bucket-name --prefix path/to/logs`
     * CORRECT: `aws s3 ls s3://bucket-name/path/to/logs/`
     * For downloading files: `aws s3 cp s3://bucket-name/path/to/object /local/destination --profile profile-name --region region-name`
     * Always include the bucket name in the path with s3:// prefix
   
   - **CloudTrail Operations**:
     * Use proper case for lookup attributes: `AttributeKey=ResourceName,AttributeValue=resource-id` (camel case, not kebab-case)
     * Example: `aws cloudtrail lookup-events --lookup-attributes AttributeKey=ResourceName,AttributeValue=i-1234567890abcdef0`
     * Remember that lookup-attributes is an array of objects with specific key names
   
   - **CloudWatch Logs**:
     * For querying logs: `aws logs get-log-events --log-group-name "/aws/group-name" --log-stream-name "stream-name"`
     * For searching logs: `aws logs filter-log-events --log-group-name "/aws/group-name" --filter-pattern "search term"`
   
   - **General Tips**:
     * For complex JSON parameters, consider using a file with the `file://` prefix
     * Always specify the region for your resources
     * Use `--query` parameter to filter large responses

## Common Failure Patterns

### SSM Document Worker Failures
1. Look for SSM document worker crashes, especially during reboot operations:
   - Check for messages like "document process failed unexpectedly" or "ipc messaging received timeout signal"
   - These often indicate that an SSM command (like reboot) failed to complete properly
   - The test may continue as if the operation succeeded when it actually failed

2. When investigating reboot-related failures:
   - Verify if the reboot actually occurred by checking for reboot sequences in logs
   - Look for gaps in timestamps that would indicate a successful reboot
   - Check if services were properly restarted after the supposed reboot
   - Remember that SSM commands that trigger reboots will appear to "fail" normally because the connection is interrupted

### Node Rejoin Failures
1. For tests that expect nodes to rejoin a cluster after reboot:
   - Confirm the reboot actually occurred (see SSM Document Worker Failures)
   - Check if any auto-rejoin mechanism exists and is properly configured
   - Verify network connectivity after reboot
   - Look for errors in the node registration process

2. Common causes of node rejoin failures:
   - SSM document worker crashes preventing proper reboot
   - Missing or corrupted configuration after reboot
   - Network connectivity issues
   - Authentication or credential problems
   - Cloud-init clean operations removing necessary configuration

### Resource State Transition Failures
1. For failures related to resource state transitions:
   - Check CloudTrail for the complete sequence of state change events
   - Look for unusual or non-standard states (e.g., "unknown-running" for EC2)
   - Identify operations attempted during state transitions that might cause failures
   - Pay attention to the timing between state change events and operation attempts
   - Examine if operations are being attempted too soon after state changes

2. Common patterns in state transition failures:
   - Operations attempted during instance reboots
   - API calls made during resource initialization or termination
   - Duplicate operations where the first succeeds but the second fails
   - Resource entering an inconsistent state in the control plane
