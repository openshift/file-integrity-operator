FROM brew.registry.redhat.io/rh-osbs/openshift-golang-builder:v1.22 as builder

COPY . .
WORKDIR bundle-hack

ARG FIO_OLD_VERSION="1.3.5"
ARG FIO_NEW_VERSION="1.3.6"

RUN go run ./update_csv.go ../bundle/manifests ${FIO_OLD_VERSION} ${FIO_NEW_VERSION}
RUN ./update_bundle_annotations.sh

FROM scratch

LABEL name="compliance/openshift-file-integrity-operator-bundle"
LABEL cpe="cpe:/a:redhat:openshift_file_integrity_operator:1::el9"
LABEL version=${FIO_NEW_VERSION}
LABEL summary='OpenShift File Integrity Operator'
LABEL maintainer='Infrastructure Security and Compliance Team <isc-team@redhat.com>'

LABEL io.k8s.display-name='File Integrity Operator'
LABEL io.k8s.description='File Integrity Operator'
LABEL description='File Integrity Operator'
LABEL vendor="Red Hat, Inc."
LABEL release=${FIO_NEW_VERSION}
LABEL url="https://github.com/openshift/file-integrity-operator"
LABEL distribution-scope=public
LABEL com.redhat.component=openshift-file-integrity-operator-bundle-container
LABEL com.redhat.delivery.appregistry=false
LABEL com.redhat.delivery.operator.bundle=true
LABEL com.redhat.openshift.versions="v4.10"

LABEL io.openshift.maintainer.product='OpenShift Container Platform'
LABEL io.openshift.tags=openshift,security,compliance,integrity

LABEL operators.operatorframework.io.bundle.channel.default.v1=stable
LABEL operators.operatorframework.io.bundle.channels.v1=stable
LABEL operators.operatorframework.io.bundle.manifests.v1=manifests/
LABEL operators.operatorframework.io.bundle.mediatype.v1=registry+v1
LABEL operators.operatorframework.io.bundle.metadata.v1=metadata/
LABEL operators.operatorframework.io.bundle.package.v1=file-integrity-operator

LABEL License=GPLv2+

# Copy files to locations specified by labels.
COPY --from=builder bundle/manifests /manifests/
COPY --from=builder bundle/metadata /metadata/
COPY bundle/tests/scorecard /tests/scorecard
