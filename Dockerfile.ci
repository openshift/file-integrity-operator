# Step one: build file-integrity-operator
FROM registry.ci.openshift.org/openshift/release:rhel-9-release-golang-1.22-openshift-4.17 AS builder
USER root

WORKDIR /go/src/github.com/openshift/file-integrity-operator

ENV GOFLAGS="-mod=vendor"

RUN dnf -y install git make gcc automake autoconf libtool flex gettext-devel e2fsprogs-devel audit-libs-devel libattr-devel flex bison zlib-devel libgcrypt-devel audit-libs-devel libacl-devel libselinux-devel libtool && dnf clean all

COPY . .

RUN git clone https://github.com/autoconf-archive/autoconf-archive.git && \
    cp autoconf-archive/m4/*.m4 /usr/share/aclocal/ 

RUN make build

RUN make aide-0.18

# Step two: containerize file-integrity-operator and AIDE together
FROM registry.fedoraproject.org/fedora-minimal:37

# Install AIDE 0.16 and dependencies
RUN microdnf -y install aide-0.16 golang && microdnf clean all

ENV OPERATOR=/usr/local/bin/file-integrity-operator \
    USER_UID=1001 \
    USER_NAME=file-integrity-operator

RUN mkdir -p /usr/local/libs

# install operator binary
COPY --from=builder /go/src/github.com/openshift/file-integrity-operator/build/bin/manager ${OPERATOR}
COPY build/bin /usr/local/bin
COPY --from=builder /go/src/github.com/openshift/file-integrity-operator/build/bin/aide-0.18 /usr/sbin/aide-0.18
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
