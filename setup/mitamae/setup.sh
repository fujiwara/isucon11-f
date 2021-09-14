#!/bin/sh

set -xe
export DEBIAN_FRONTEND=noninteractive
apt-get update && apt-get -y install build-essential wget curl git zip gnupg2 lsb-release

curl -sL -o /usr/bin/mitamae https://github.com/itamae-kitchen/mitamae/releases/download/v1.12.7/mitamae-x86_64-linux
chmod +x /usr/bin/mitamae
mitamae version

mkdir -p ~/.ssh
echo "Host github.com" >> ~/.ssh/config
echo "  Compression yes" >> ~/.ssh/config
echo "Setup completed."
