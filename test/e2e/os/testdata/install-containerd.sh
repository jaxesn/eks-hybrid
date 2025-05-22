#!/usr/bin/env bash

set -o errexit
set -o nounset
set -o pipefail

if command -v apt-get >/dev/null 2>&1; then
    CMD="apt-get -o DPkg::Lock::Timeout=60"
    LOCK_FILE="/var/lib/dpkg/lock-frontend"
    PACKAGE="containerd.io=1.*"
else
    CMD="yum"
    LOCK_FILE="/var/lib/rpm/.rpm.lock"
    PACKAGE="containerd.io-1.*"
fi

ATTEMPTS=5
while ! $CMD install -y $PACKAGE ; do
    # attempt to wait for any in progress apt-get/yum operations to complete
    while find /proc/*/fd -ls | grep $LOCK_FILE >/dev/null 2>&1; do
        echo "waiting for process with lock on $LOCK_FILE to complete"
        sleep 1
    done

    ((ATTEMPTS--)) || break
    sleep 5
done

if ! command -v ctr >/dev/null 2>&1; then 
    echo "containerd failed to installed"
    exit 1
fi
