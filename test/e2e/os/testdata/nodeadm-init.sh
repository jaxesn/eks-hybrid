#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

NODEADM_URL="$1"
KUBERNETES_VERSION="$2"
PROVDER="$3"
REGION="$4"
NODEADM_ADDITIONAL_ARGS="${5-}"

# nodeadmin uninstall does not remove this folder, which contains the cilium/calico config
# which kubelet uses to determine if a node is "Ready"
# if we do not remove this folder, the node will flip to ready on re-join immediately
# removing on boot instead of before rebooting to ensure containers are no longer running
# so deletion succeeds and is not replaced by the running container
rm -rf /etc/cni/net.d

echo "Downloading nodeadm binary"
for i in {1..5}; do curl --fail -s --retry 5 -L "$NODEADM_URL" -o /tmp/nodeadm-bin && break || sleep 5; done

chmod +x /tmp/nodeadm-bin
mv /tmp/nodeadm-wrapper.sh /tmp/nodeadm


echo "Installing kubernetes components"
/tmp/nodeadm install $KUBERNETES_VERSION $NODEADM_ADDITIONAL_ARGS --credential-provider $PROVDER --region $REGION

echo "Initializing the node"
/tmp/nodeadm init -c file:///nodeadm-config.yaml
