# Cloud Tasks Emulator

**Google Cloud Tasks does not provide an official local emulator. This project fills that gap.**

A lightweight, single-binary local emulator for [Google Cloud Tasks](https://cloud.google.com/tasks) that mimics the core REST and gRPC APIs for local development. SQLite-backed, Dockerized, and ready for your existing Cloud Tasks clients.

<!-- Add docs/screenshot.png for Admin UI preview -->

## Features

- **Queue Management**: Create, list, get, and delete queues
- **Task Management**: Create, list, get, and delete tasks
- **Task Execution**: Background worker (500ms poll), HTTP dispatch, exponential backoff retries
- **Scheduling**: Respects `schedule_time` and `next_attempt_at`
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

## Admin UI

Open http://localhost:8085/ui/queues to:

- List and create queues
- View tasks per queue with status, method, URL, scheduled time
- Filter by status (Pending, Running, Completed, Failed)
- Click task ID to view payload, headers, and last error
- Retry failed tasks
- Trigger tasks immediately

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
