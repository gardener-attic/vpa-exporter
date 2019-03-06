# vpa-exporter
Vertical pod autoscaler ([VPA](https://github.com/kubernetes/autoscaler/tree/master/vertical-pod-autoscaler)) frees up users from having to update the resource requests for containers in the pods. It sets the requests based on usage. This enables proper scheduling by ensuring that appropriate resource amount is available for each pod.

A `VerticalPodAutoscaler` resource allows to specify which pods should be vertically autoscaled as well as if/how the resource recommendations are applied.  The resource requests that are updated in the containers of the pod are computed by the VPA based on historical resource usage. The resource suggestions are available in the `status` of the `VerticalPodAutoscaler` resource.

`vpa-exporter` exposes the `/metrics` endpoint which can be used by Prometheus to  scrape `VPA` metadata information and suggestions.

## Table of Contents

- [Prerequisites](#prerequisites)
- [Build](#build)
- [Dependency management](#dependency-management)
  - [Updating dependencies](#updating-dependencies)
- [Usage](#usage)

### Prerequisites

Although the following installation instructions are for Mac OS X, similar alternate commands could be found for any Linux distribution

#### Installing [Golang](https://golang.org/) environment

Install the latest version of Golang (at least `v1.9.4` is required). For Mac OS, you could use [Homebrew](https://brew.sh/):

```sh
brew install golang
```

For other OS, please check [Go installation documentation](https://golang.org/doc/install).

Make sure to set your `$GOPATH` environment variable properly (conventionally, it points to `$HOME/go`).

For your convenience, you can add the `bin` directory of the `$GOPATH` to your `$PATH`: `PATH=$PATH:$GOPATH/bin`, but it is not necessarily required.

We use [Dep](https://github.com/golang/dep) for managing golang package dependencies. Please install it
on Mac OS via

```sh
brew install dep
```

On other operating systems, please check the [Dep installation documentation](https://golang.github.io/dep/docs/installation.html) and the [Dep releases page](https://github.com/golang/dep/releases). After downloading the appropriate release in your `$GOPATH/bin` folder, you need to make it executable via `chmod +x <dep-release>` and rename it to dep via `mv dep-<release> dep`.

#### [Golint](https://github.com/golang/lint)

In order to perform linting on the Go source code, please install [Golint](https://github.com/golang/lint):

```bash
go get -u github.com/golang/lint/golint
```

#### Installing `git`

We use `git` as VCS which you would need to install.

On Mac OS run

```sh
brew install git
```

#### Installing `Docker` (Optional)

In case you want to build Docker images, you have to install Docker itself. We recommend using [Docker for Mac OS X](https://docs.docker.com/docker-for-mac/) which can be downloaded from [here](https://download.docker.com/mac/stable/Docker.dmg).

### Build

First, you need to create a target folder structure before cloning and building `vpa-exporter`.

```sh

mkdir -p ~/go/src/github.com/gardener
cd ~/go/src/github.com/gardener
git clone https://github.com/gardener/vpa-exporter.git
cd vpa-exporter
```

To build the binary in your local machine environment, use `make` target `build-local`.

```sh
make build-local
```

This will build the binary `vpa-exporter` under the `bin` directory.

Next you can make it available to use as shell command by moving the executable to `/usr/local/bin`.

### Dependency management

We use [Dep](https://github.com/golang/dep) to manage golang dependencies.. In order to add a new package dependency to the project, you can perform `dep ensure -add <PACKAGE>` or edit the `Gopkg.toml` file and append the package along with the version you want to use as a new `[[constraint]]`.

#### Updating dependencies

The `Makefile` contains a rule called `revendor` which performs a `dep ensure -update` and a `dep prune` command. This updates all the dependencies to its latest versions (respecting the constraints specified in the `Gopkg.toml` file). The command also installs the packages which do not already exist in the `vendor` folder but are specified in the `Gopkg.toml` (in case you have added new ones).

```sh
make revendor
```

The dependencies are installed into the `vendor` folder which **should be added** to the VCS.

:warning: Make sure you test the code after you have updated the dependencies!

### Usage

Use the `help` option of the `vpa-exporter` command to show usage details.

```sh
vpa-exporter --help
Usage of vpa-exporter:
  -kubeconfig string
    	Path to a kubeconfig. Only required if out-of-cluster.
  -master string
    	The address of the Kubernetes API server. Overrides any value in kubeconfig. Only required if out-of-cluster.
```