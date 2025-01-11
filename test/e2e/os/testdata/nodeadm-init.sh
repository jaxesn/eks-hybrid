#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

NODEADM_URL="$1"
KUBERNETES_VERSION="$2"
PROVDER="$3"
NODEADM_ADDITIONAL_ARGS="${4-}"

function gather_logs(){
    /tmp/nodeadm debug -c file:///nodeadm-config.yaml || true
    # Arbitrary wait to give enough time for logs to populated with potential errors
    # if the node successfully joins and reboots in this, we wont get the logs
    sleep 15
    /tmp/log-collector.sh "post-install" "post-uninstall-install"
}

trap "gather_logs" EXIT

echo "Downloading nodeadm binary"
for i in {1..5}; do curl --fail -s --retry 5 -L "$NODEADM_URL" -o /tmp/nodeadm && break || sleep 5; done

chmod +x /tmp/nodeadm

echo "Installing kubernetes components"
/tmp/nodeadm install $KUBERNETES_VERSION $NODEADM_ADDITIONAL_ARGS --credential-provider $PROVDER

echo "Initializing the node"
/tmp/nodeadm init -c file:///nodeadm-config.yaml
