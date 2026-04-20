#!/usr/bin/env bash
#!/usr/bin/env bash
set -e
export PAGER=cat
export GIT_PAGER=cat
export AWS_PAGER=""

LOCALSTACK_CLI="env AWS_ACCESS_KEY_ID=test AWS_SECRET_ACCESS_KEY=test AWS_DEFAULT_REGION=us-east-1 aws --endpoint-url=http://localhost:4566"

echo "==> Resetting state..."
rm -f log-sentinel-examples.db log-sentinel-examples.db-wal log-sentinel-examples.db-shm
git checkout examples/

echo "==> Starting ministack..."
docker compose up -d
echo "==> Waiting for ministack to be ready..."
until $LOCALSTACK_CLI sqs list-queues &>/dev/null; do sleep 1; done

echo "==> Creating/purging SQS queue..."
$LOCALSTACK_CLI sqs create-queue --queue-name log-sentinel --output text --query 'QueueUrl' 2>/dev/null \
  || true
$LOCALSTACK_CLI sqs purge-queue --queue-url http://localhost:4566/000000000000/log-sentinel 2>/dev/null \
  || true

echo "==> Building..."
go build -o /tmp/log-sentinel ./cmd/sentinel

echo "==> Running examples (press q or Ctrl+C to quit)..."
echo "    Workers will call 'claude --print --dangerously-skip-permissions' to fix bugs."
echo "    Fixes are applied in-place — no git commits will be made."
echo ""
/tmp/log-sentinel --config config.examples.yaml --db log-sentinel-examples.db
