# AURA Android build environment (Blueprint D.1 P7, ADR-0052). AURA's own toolchain, built only from pinned upstream artifacts.
FROM eclipse-temurin:17-jdk-jammy
RUN apt-get update && apt-get install -y --no-install-recommends unzip ca-certificates && rm -rf /var/lib/apt/lists/*
ENV ANDROID_HOME=/opt/android-sdk GRADLE_HOME=/opt/gradle
ADD --checksum=sha256:2d2d50857e4eb553af5a6dc3ad507a17adf43d115264b1afc116f95c92e5e258 \
    https://dl.google.com/android/repository/commandlinetools-linux-11076708_latest.zip /tmp/clt.zip
ADD --checksum=sha256:31c55713e40233a8303827ceb42ca48a47267a0ad4bab9177123121e71524c26 \
    https://services.gradle.org/distributions/gradle-8.10.2-bin.zip /tmp/gradle.zip
RUN mkdir -p $ANDROID_HOME/cmdline-tools && unzip -q /tmp/clt.zip -d $ANDROID_HOME/cmdline-tools \
 && mv $ANDROID_HOME/cmdline-tools/cmdline-tools $ANDROID_HOME/cmdline-tools/latest \
 && unzip -q /tmp/gradle.zip -d /opt && mv /opt/gradle-8.10.2 $GRADLE_HOME && rm /tmp/*.zip \
 && yes | $ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager --licenses >/dev/null \
 && $ANDROID_HOME/cmdline-tools/latest/bin/sdkmanager "platforms;android-35" "build-tools;35.0.0" "build-tools;34.0.0" "platform-tools" >/dev/null
ENV PATH=$GRADLE_HOME/bin:$ANDROID_HOME/build-tools/35.0.0:$PATH
WORKDIR /src
