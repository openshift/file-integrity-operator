# Step one: build file-integrity-operator
FROM registry.access.redhat.com/ubi8/go-toolset as builder
USER root

WORKDIR /go/src/github.com/openshift/file-integrity-operator

ENV GOFLAGS="-mod=vendor"

COPY . .

RUN make operator-bin

# Step two: containerize file-integrity-operator and AIDE together
FROM registry.centos.org/centos:8
RUN yum -y install aide && yum clean all

ENV OPERATOR=/usr/local/bin/file-integrity-operator \
    USER_UID=1001 \
    USER_NAME=file-integrity-operator

# install operator binary
COPY --from=builder /go/src/github.com/openshift/file-integrity-operator/build/_output/bin/file-integrity-operator ${OPERATOR}
COPY build/bin /usr/local/bin
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
