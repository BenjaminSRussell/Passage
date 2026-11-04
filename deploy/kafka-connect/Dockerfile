# Kafka Connect image for deploy/kafka/connect.yaml: Strimzi's Kafka 4.0.0 image plus the Debezium
# Postgres connector and the passage-cloudevents-enricher SMT (enricher/). Built and pushed by the
# `connect-image` job in .github/workflows/ci.yml; build context is this directory.
# Changing a version here means changing the image tag in deploy/kafka/connect.yaml and ci.yml.

FROM maven:3.9.9-eclipse-temurin-17@sha256:f58d59b6273e785ac0a4477f6e9b5ba1d7731c75b906c0f7b34076f1851318cc AS enricher
WORKDIR /src
COPY enricher/pom.xml .
RUN mvn -q -B dependency:go-offline
COPY enricher/src src
# CI runs `mvn verify` (tests included) in the enricher job before this image is built.
RUN mvn -q -B package -Dmaven.test.skip=true

FROM maven:3.9.9-eclipse-temurin-17@sha256:f58d59b6273e785ac0a4477f6e9b5ba1d7731c75b906c0f7b34076f1851318cc AS debezium
ARG DEBEZIUM_VERSION=3.0.7.Final
ARG DEBEZIUM_SHA256=a89243b29e4fe407e67444be235630547a77a1d430d529354750a6f9c2873598
RUN curl -fsSLo /tmp/debezium.tgz \
      "https://repo1.maven.org/maven2/io/debezium/debezium-connector-postgres/${DEBEZIUM_VERSION}/debezium-connector-postgres-${DEBEZIUM_VERSION}-plugin.tar.gz" \
 && echo "${DEBEZIUM_SHA256}  /tmp/debezium.tgz" | sha256sum --check --strict \
 && mkdir -p /plugins \
 && tar -xzf /tmp/debezium.tgz -C /plugins

FROM quay.io/strimzi/kafka:0.46.0-kafka-4.0.0@sha256:e0a017c707e101d373d992061e4531db22da7a80bac1a5af9ad8c2ffd604c41d
USER root:root
COPY --from=debezium /plugins/debezium-connector-postgres /opt/kafka/plugins/debezium-postgres
COPY --from=enricher /src/target/passage-cloudevents-enricher-0.1.0.jar /opt/kafka/plugins/passage-cloudevents-enricher/
USER 1001
