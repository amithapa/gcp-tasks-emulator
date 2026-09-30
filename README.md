# Cloud Tasks Emulator

**Google Cloud Tasks does not provide an official local emulator. This project fills that gap.**

A lightweight, single-binary local emulator for [Google Cloud Tasks](https://cloud.google.com/tasks) that mimics the core REST and gRPC APIs for local development. SQLite-backed, Dockerized, and ready for your existing Cloud Tasks clients.

<!-- Add docs/screenshot.png for Admin UI preview -->

## Features

- **Queue Management**: Create, list, get, and delete queues
- **Task Management**: Create, list, get, and delete tasks
- **Task Execution**: Background worker (500ms poll), HTTP dispatch, exponential backoff retries
- **Scheduling**: Respects `schedule_time` and `next_attempt_at`
- **Cloud Scheduler emulation**: cron jobs (REST `/v1/.../jobs`, gRPC `CloudScheduler`, UI at `/ui/jobs`) that fire HTTP requests on schedule
- **Admin UI**: Server-rendered HTML at `/ui/queues` — view queues, tasks, retry failed, trigger immediately
- **gRPC API**: Full Cloud Tasks v2 gRPC compatibility for Python, Node, Go SDKs
- **REST API**: Cloud Tasks–style HTTP endpoints

## What It Does NOT Do

- IAM or authentication
- Distributed scaling or regional replication
- OIDC token validation
- Full Cloud Tasks feature parity

## Quick Start

```bash
# Docker (recommended)
docker run -p 8085:8085 -p 9090:9090 yourname/cloud-tasks-emulator

# Or locally
make run
```

Then open http://localhost:8085/ui/queues

## API Examples

### Create a Queue

```bash
curl -X POST http://localhost:8085/v2/projects/local-project/locations/us-central1/queues \
  -H "Content-Type: application/json" \
  -d '{"queue": {"name": "my-queue"}}'
```

### Create a Task

```bash
curl -X POST http://localhost:8085/v2/projects/local-project/locations/us-central1/queues/my-queue/tasks \
  -H "Content-Type: application/json" \
  -d '{
    "task": {
      "httpRequest": {
        "httpMethod": "POST",
        "url": "http://host.docker.internal:3000/webhook",
        "headers": {"Content-Type": "application/json"},
        "body": "'$(echo -n '{"key":"value"}' | base64)'"
      }
    }
  }'
```

### List Tasks

```bash
curl http://localhost:8085/v2/projects/local-project/locations/us-central1/queues/my-queue/tasks
```

### Health Check

```bash
curl http://localhost:8085/health
```

## Using With Your Backend

### gRPC (Python, Node, Go SDKs)

Point your Cloud Tasks client to the emulator's gRPC endpoint (`localhost:9090`):

```python
# Python
from google.cloud.tasks_v2 import CloudTasksAsyncClient
from google.cloud.tasks_v2.services.cloud_tasks.transports import CloudTasksGrpcAsyncIOTransport
import grpc

if settings.cloud_tasks_emulator_host:  # e.g. "localhost:9090"
    channel = grpc.aio.insecure_channel(settings.cloud_tasks_emulator_host)
    transport = CloudTasksGrpcAsyncIOTransport(channel=channel)
    client = CloudTasksAsyncClient(transport=transport)
else:
    client = CloudTasksAsyncClient()
```

### REST API

Use `http://localhost:8085/v2` as the base URL for Cloud Tasks REST calls.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | 8085 | HTTP server port |
| `GRPC_PORT` | 9090 | gRPC server port |
| `DATABASE_PATH` | ./tasks.db | SQLite database path |
| `AUTO_CREATE_QUEUES` | true | Create queue if missing when creating a task |
| `DEFAULT_PROJECT` | local-project | Default GCP project |
| `DEFAULT_LOCATION` | us-central1 | Default GCP location |
| `WORKER_CONCURRENCY` | 10 | Max concurrent task dispatches |
| `WORKER_POLL_INTERVAL_MS` | 500 | Worker poll interval |
| `DEFAULT_MAX_RETRIES` | 5 | Max retries per task |
| `SCHEDULER_ENABLED` | true | Run the Cloud Scheduler job runner |
| `SCHEDULER_POLL_INTERVAL_MS` | 1000 | How often the scheduler checks for due jobs |

## Authenticating to your targets

If your target verifies a service-account JWT, set one of these and the emulator adds
`Authorization: Bearer <token>` to every task and scheduler request (unless the task/job already sets its own `Authorization` header):

| Variable | Description |
|----------|-------------|
| `SERVICE_ACCOUNT_TOKEN` | Static token, sent as-is. Wins if both are set. It stops working when the JWT expires. |
| `SERVICE_ACCOUNT_JWT_SECRET` | HS256 secret. A fresh 1h token is minted per request with `sub=service-account`, `role=service_role`, `aud`, `iat`, `exp`, `email`. |
| `SERVICE_ACCOUNT_EMAIL` | `email` claim when minting (default `local-dev@example.com`) |
| `SERVICE_ACCOUNT_AUDIENCE` | `aud` claim when minting (default `authenticated`) |

The token is the same for every target. Real Cloud Tasks per-task `oidcToken`/`oauthToken` settings are still ignored.

## Admin UI

Open http://localhost:8085/ui/queues to:

- List and create queues
- View tasks per queue with status, method, URL, scheduled time
- Filter by status (Pending, Running, Completed, Failed)
- Click task ID to view payload, headers, and last error
- Retry failed tasks
- Trigger tasks immediately

## Cloud Scheduler Emulation

Jobs are stored in the same SQLite database and fired by an in-process runner that
sends the HTTP request directly to the job's target (it does not go through a queue).
Supported: HTTP targets, 5-field cron plus descriptors (`@hourly`, `@daily`, `@every 30s`),
IANA time zones (default `UTC`), pause/resume/run, `retryConfig` (count, min/max backoff,
max doublings, max retry duration) and `attemptDeadline` (15s to 30m, default 180s).
The same job never runs concurrently with itself, and after downtime a job fires once
and then resumes its normal schedule (no catch-up storm).

```bash
# Create (body is a Cloud Scheduler Job; timeZone defaults to UTC)
curl -X POST http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs \
  -H "Content-Type: application/json" \
  -d '{"name":"projects/local-project/locations/us-central1/jobs/ping","schedule":"*/5 * * * *","timeZone":"America/New_York",
       "httpTarget":{"uri":"http://localhost:3000/hook","httpMethod":"POST","headers":{"X-Key":"v"},"body":"aGVsbG8="}}'

curl http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs          # list
curl http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping     # get
curl -X PATCH "http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping?updateMask=schedule" \
  -H "Content-Type: application/json" -d '{"schedule":"@hourly"}'
curl -X POST http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping:pause
curl -X POST http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping:resume
curl -X POST http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping:run
curl -X DELETE http://localhost:8085/v1/projects/local-project/locations/us-central1/jobs/ping
```

The gRPC `google.cloud.scheduler.v1.CloudScheduler` service is served on the same gRPC port
(`GRPC_PORT`), so the official Cloud Scheduler client libraries work when pointed at
`localhost:9090` with an insecure connection. Each request carries `X-CloudScheduler`,
`X-CloudScheduler-JobName`, `X-CloudScheduler-ScheduleTime` and `X-CloudScheduler-AttemptNumber` headers.
Manage jobs in the browser at http://localhost:8085/ui/jobs.

Limitations: only HTTP targets (no Pub/Sub or App Engine targets), no OIDC/OAuth auth headers,
no pagination, and an explicit `maxDoublings` of 0 is treated as the default (5).

## Docker

```bash
# Build
make docker

# Run with persistent data (HTTP on 8085, gRPC on 9090)
docker run -p 8085:8085 -p 9090:9090 -v ./data:/app/data cloud-tasks-emulator:latest

# With custom ports
docker run -p 9000:8085 -p 9091:9090 -e PORT=8085 -e GRPC_PORT=9090 -v ./data:/app/data cloud-tasks-emulator:latest
```

Mount a volume to `/app/data` to persist the SQLite database.

## Development

```bash
make run    # Start server
make test   # Run tests
make build  # Build binary
make lint   # Run golangci-lint
```

## License

MIT
