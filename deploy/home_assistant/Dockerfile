# The component as an init-container's source: busybox's cp and /cadence, nothing else.
ARG BUSYBOX_IMAGE=busybox:1.37.0-glibc@sha256:42a9ff19af33811ee466b24c66384461d37c32dcc28d1d27d49b8aca816bd5ee
FROM ${BUSYBOX_IMAGE}
COPY --link integrations/home_assistant/custom_components/cadence /cadence
