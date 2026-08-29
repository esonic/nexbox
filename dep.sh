#!/usr/bin/env bash
set -e

./build.sh
mv /home/ubuntu/src/nexbox/dist/nexbox ~/nex/
cd ~/nex
./run.sh
