#!/usr/bin/env bash
AWS_ENDPOINT_URL_EKS={{ .EKSEndpoint }} AWS_MAX_ATTEMPTS=10 /tmp/nodeadm-bin "$@"