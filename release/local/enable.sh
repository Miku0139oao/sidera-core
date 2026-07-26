#!/usr/bin/env bash

set -e -o pipefail

sudo systemctl enable sidera
sudo systemctl start sidera
sudo journalctl -u sidera --output cat -f
