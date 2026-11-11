// AURA Android client (Blueprint D.1 P7, ADR-0052, ADR-0060). A client of the public API (AGENTS rule 17); it holds a
// member token, never the owner token. The interface is Jetpack Compose (Apache-2.0, ADR-0060); there is no web view
// and no code that arrives over the network: AURA's screens and replies are data the app draws with its own components.
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.compose")
    id("app.cash.paparazzi") // test-only: draws the screens on the build machine, no device needed
}

val auraVersion: String = (System.getenv("AURA_APP_VERSION") ?: "0.1.0")
val auraCode: Int = (System.getenv("AURA_APP_CODE") ?: "1").toInt()

android {
    namespace = "ai.aura.client"
    compileSdk = 35
    defaultConfig {
        applicationId = "ai.aura.client"
        minSdk = 26
        targetSdk = 35
        versionCode = auraCode
        versionName = auraVersion
    }
    signingConfigs {
        create("release") {
            val ks = System.getenv("AURA_KEYSTORE")
            if (ks != null) {
                val pass = file(System.getenv("AURA_KEYSTORE_PASSWORD_FILE")).readText().trim() // a file, never an env value
                storeFile = file(ks)
                storePassword = pass
                keyAlias = "aura"
                keyPassword = pass
            }
        }
    }
    buildTypes {
        getByName("release") {
            isMinifyEnabled = true
            isShrinkResources = true
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"))
            signingConfig = signingConfigs.getByName("release")
        }
    }
    buildFeatures { compose = true }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    testOptions { unitTests.isReturnDefaultValues = true }
    lint { abortOnError = true; checkReleaseBuilds = true; disable += setOf("GradleDependency", "OldTargetApi", "SetTextI18n", "AndroidGradlePluginVersion") }
}

dependencies {
    implementation(platform("androidx.compose:compose-bom:2024.12.01"))
    implementation("androidx.activity:activity-compose:1.9.3")
    implementation("androidx.compose.ui:ui")
    implementation("androidx.compose.foundation:foundation")
    implementation("androidx.compose.animation:animation")
    implementation("androidx.compose.material3:material3")
    implementation("androidx.compose.material:material-icons-core")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.9.0")
    testImplementation("junit:junit:4.13.2") // test-only, never shipped
    testImplementation("org.json:json:20240303") // the platform's org.json is a stub in JVM unit tests
}
