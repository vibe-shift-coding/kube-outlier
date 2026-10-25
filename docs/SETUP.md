# Local 3-cluster test fleet

The walkthrough in [RUNNING.md](RUNNING.md) and `scripts/fleet-smoke.sh` use three
minikube clusters named `c1`, `c2` and `c3`, each running as a container in Docker. You
normally don't need this page: `scripts/fleet-up.sh` sets everything up (and is safe to
re-run), and `scripts/fleet-down.sh` stops it again. This page explains what they need
and what they do.

## What you need

- [`kubectl`](https://kubernetes.io/docs/tasks/tools/) and
  [`minikube`](https://minikube.sigs.k8s.io/docs/start/).
- A running Docker daemon with about 6 GiB of memory to spare (three clusters × 2 GiB).
  Any of these works:
  - **Linux:** Docker Engine (your user must be able to run `docker` without `sudo`).
  - **macOS or Windows:** Docker Desktop, or on macOS OrbStack or Colima.

`fleet-up.sh` uses whichever Docker is already running. If none is running but Colima is
installed, it starts Colima for you and `fleet-down.sh` stops it again afterwards.

Install with your usual package manager. For example, on macOS with Homebrew:

```sh
brew install kubectl minikube colima docker
```

## What `fleet-up.sh` does

If no Docker daemon is running and Colima is installed, it starts a Colima VM sized for
the three clusters. On Apple silicon it adds `--arch aarch64 --vm-type=vz --vz-rosetta`
for native virtualization with x86 emulation:

```sh
colima start --cpu 6 --memory 8
```

Then it starts the three clusters. `--driver=docker` runs each as a container, much like
`kind`:

```sh
minikube start -p c1 --driver=docker --memory=2048 --cpus=2
minikube start -p c2 --driver=docker --memory=2048 --cpus=2
minikube start -p c3 --driver=docker --memory=2048 --cpus=2
```

Each `minikube start` adds a kubectl context with the same name. Check they're up:

```sh
minikube -p c1 status
minikube -p c2 status
minikube -p c3 status
```

## What `fleet-down.sh` does

It stops the clusters, which keep their state and start again with `minikube start`:

```sh
minikube stop -p c1
minikube stop -p c2
minikube stop -p c3
```

With `--delete` it removes them entirely instead:

```sh
minikube delete -p c1
minikube delete -p c2
minikube delete -p c3
```

Finally, if `fleet-up.sh` started Colima, it stops it:

```sh
colima stop
```
