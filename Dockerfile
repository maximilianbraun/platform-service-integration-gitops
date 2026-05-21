FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY bin/platform-service-integration-gitops /manager
USER 65532:65532
ENTRYPOINT ["/manager"]
