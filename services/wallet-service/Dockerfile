FROM eclipse-temurin:26-jdk-alpine AS build
WORKDIR /app
COPY pom.xml .
COPY mvnw .
COPY .mvn .mvn
COPY src ./src
RUN --mount=type=cache,target=/root/.m2 \
    ./mvnw -q package -DskipTests

# Unpack the fat jar into <name>.jar + lib/ so dependencies get their own layer.
RUN java -Djarmode=tools -jar target/*.jar extract --destination /extracted \
 && mv /extracted/*.jar /extracted/app.jar

# Build a trimmed JRE containing only the modules the app (and its libs) use.
RUN MODULES="$(jdeps --ignore-missing-deps --multi-release 26 --print-module-deps \
        --class-path '/extracted/lib/*' /extracted/app.jar)" \
 && jlink --add-modules "${MODULES},jdk.unsupported,jdk.crypto.ec,java.management,java.naming,java.sql,java.instrument" \
        --strip-debug --no-header-files --no-man-pages --compress=zip-9 \
        --output /jre

FROM alpine:3.22
ENV JAVA_HOME=/opt/jre PATH="/opt/jre/bin:${PATH}"
WORKDIR /app
COPY --from=build /jre /opt/jre
COPY --from=build /extracted/lib ./lib
COPY --from=build /extracted/app.jar ./app.jar
EXPOSE 8080
ENTRYPOINT ["java", "-jar", "app.jar"]
