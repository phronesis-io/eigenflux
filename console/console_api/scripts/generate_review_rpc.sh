#!/bin/bash
set -euo pipefail
cd "$(dirname "$0")/.."
kitex -module console.eigenflux.ai idl/commission_review.thrift
