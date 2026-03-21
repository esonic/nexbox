#!/bin/bash

export CLAUDE_CODE_OAUTH_TOKEN=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
export HTTP_PROXY="http://esonic:comaz@38.145.211.211:8081"
target/boxnex --port 9042 --listen 0.0.0.0
