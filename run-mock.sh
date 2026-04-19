#!/usr/bin/env bash
set -e
export PAGER=cat
export GIT_PAGER=cat
export AWS_PAGER=""

AWS_CLI="aws --endpoint-url=http://localhost:4566"
export AWS_ACCESS_KEY_ID=test
export AWS_SECRET_ACCESS_KEY=test
export AWS_DEFAULT_REGION=us-east-1

echo "==> Starting ministack..."
docker compose up -d
echo "==> Waiting for ministack to be ready..."
until $AWS_CLI sqs list-queues &>/dev/null; do sleep 1; done

echo "==> Creating SQS queue..."
$AWS_CLI sqs create-queue --queue-name log-sentinel --output text --query 'QueueUrl' 2>/dev/null \
  || echo "    (queue already exists)"

echo "==> Building..."
go build -o /tmp/log-sentinel ./cmd/sentinel

echo "==> Running (press q or Ctrl+C to quit)..."
/tmp/log-sentinel --config config.mock.yaml
