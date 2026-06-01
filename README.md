# CloudHelper

This repository contains lightweight host status tools.

## Components

- `probe_node`: local host status snapshot tool.
- `probe_controller`: lightweight status dashboard.

## Build

```bash
cd probe_node
go build ./...
```

```bash
cd probe_controller
go build ./...
```

## Run

```bash
cd probe_node
go run . --once
```

```bash
cd probe_controller
go run . --listen 127.0.0.1:15030
```

Then open:

```text
http://127.0.0.1:15030/dashboard
```
