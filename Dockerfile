FROM alpine:3.22@sha256:49340b346798875b5592e5d420241f2f9cfba5f47e62ebb1446a1b34734e898f
RUN adduser -k /dev/null -u 10001 -D helm-schema \
  && chgrp 0 /home/helm-schema \
  && chmod -R g+rwX /home/helm-schema
COPY helm-schema /
USER 10001
VOLUME [ "/home/helm-schema" ]
WORKDIR /home/helm-schema
ENTRYPOINT ["/helm-schema"]
