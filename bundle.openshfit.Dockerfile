FROM registry.access.redhat.com/ubi9/ubi-minimal:latest as builder-runner
RUN microdnf install -y python3 python3-pip
RUN pip3 install --upgrade pip && pip3 install ruamel.yaml==0.17.9

# Use a new stage to enable caching of the package installations for local development
FROM builder-runner as builder

COPY ./bundle-hack .
COPY ./bundle/icons ./icons
COPY ./bundle/manifests ./manifests
COPY ./bundle/metadata ./metadata

RUN ./update_csv.py ./manifests 1.3.4
RUN ./update_bundle_annotations.sh

FROM scratch

LABEL name=openshift-file-integrity-operator-bundle
LABEL version=1.3.4
LABEL summary='OpenShift File Integrity Operator'
LABEL maintainer='Infrastructure Security and Compliance Team <isc-team@redhat.com>'

LABEL io.k8s.display-name='File Integrity Operator'
LABEL io.k8s.description='File Integrity Operator'

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
COPY --from=builder /manifests /manifests/
COPY --from=builder /metadata /metadata/
COPY bundle/tests/scorecard /tests/scorecard
