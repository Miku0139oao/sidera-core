#!/usr/bin/env bash

set -e -o pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
source "$SCRIPT_DIR/common.sh"

echo "Uninstalling Sidera..."

if systemctl is-active --quiet sidera 2>/dev/null; then
    echo "Stopping Sidera service..."
    sudo systemctl stop sidera
fi

if systemctl is-enabled --quiet sidera 2>/dev/null; then
    echo "Disabling Sidera service..."
    sudo systemctl disable sidera
fi

echo "Removing files..."
sudo rm -rf "$INSTALL_DATA_PATH"
sudo rm -rf "$INSTALL_BIN_PATH/$BINARY_NAME"
sudo rm -rf "$INSTALL_CONFIG_PATH"
sudo rm -rf "$SYSTEMD_SERVICE_PATH/sidera.service"

echo "Reloading systemd..."
sudo systemctl daemon-reload

echo ""
echo "Uninstallation complete!"
