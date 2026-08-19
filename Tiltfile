# Development loop against a local Kubernetes cluster.
#
# The binary is compiled on the host and synced into the running container, so
# a code change costs a compile plus a file copy instead of an image build and
# a push into the cluster.

load('ext://restart_process', 'docker_build_with_restart')

# Only local clusters. A typo in the kube context should not deploy a
# development build somewhere real.
allow_k8s_contexts([])

# The cluster nodes run on the host architecture, so the binary is built for it.
GOARCH = str(local('go env GOARCH', quiet=True)).strip()

local_resource(
    'compile',
    'CGO_ENABLED=0 GOOS=linux GOARCH=%s go build -o bin/aegisd ./cmd/server' % GOARCH,
    deps=['cmd', 'internal', 'go.mod', 'go.sum'],
    labels=['build'],
)

# restart_process wraps the entrypoint so the process is restarted after the
# sync. The plain restart_container() only works on a Docker runtime, and kind
# runs containerd.
docker_build_with_restart(
    'aegis',
    '.',
    dockerfile='docker/Dockerfile.tilt',
    entrypoint=['/app/aegisd'],
    only=['bin/aegisd'],
    live_update=[sync('bin/aegisd', '/app/aegisd')],
)

# The dev overlay relaxes exactly two things from the hardened base: the read
# only root filesystem, which would block live_update from writing the binary,
# and the replica count. Production deploys the base.
k8s_yaml(kustomize('deploy/k8s/overlays/dev'))

k8s_resource(
    'aegis',
    port_forwards=['7500:7500'],
    resource_deps=['compile'],
    labels=['app'],
)
