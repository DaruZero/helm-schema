FROM alpine:3.22@sha256:14358309a308569c32bdc37e2e0e9694be33a9d99e68afb0f5ff33cc1f695dce
RUN adduser -k /dev/null -u 10001 -D helm-schema \
  && chgrp 0 /home/helm-schema \
  && chmod -R g+rwX /home/helm-schema
COPY helm-schema /
USER 10001
VOLUME [ "/home/helm-schema" ]
WORKDIR /home/helm-schema
ENTRYPOINT ["/helm-schema"]
