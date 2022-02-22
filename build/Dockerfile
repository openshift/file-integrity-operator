# Step one: build file-integrity-operator
FROM golang:1.16 as builder
USER root

WORKDIR /go/src/github.com/openshift/file-integrity-operator

ENV GOFLAGS="-mod=vendor"

COPY . .

RUN make operator-bin

# Step two: containerize file-integrity-operator and AIDE together
FROM registry.fedoraproject.org/fedora-minimal:34
RUN microdnf -y install aide golang && microdnf clean all

ENV OPERATOR=/usr/local/bin/file-integrity-operator \
    USER_UID=1001 \
    USER_NAME=file-integrity-operator

# install operator binary
COPY --from=builder /go/src/github.com/openshift/file-integrity-operator/build/_output/bin/file-integrity-operator ${OPERATOR}
COPY build/bin /usr/local/bin
RUN  /usr/local/bin/user_setup

ENTRYPOINT ["/usr/local/bin/entrypoint"]

USER ${USER_UID}
