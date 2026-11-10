// AURA Android client (Blueprint D.1 P7, ADR-0052). No third-party runtime libraries: only the Android platform and
// the Kotlin standard library. A client of the public API (AGENTS rule 17); it holds a member token, never the owner token.
plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
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
            isMinifyEnabled = false
            signingConfig = signingConfigs.getByName("release")
        }
    }
    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }
    kotlinOptions { jvmTarget = "17" }
    testOptions { unitTests.isReturnDefaultValues = true }
    lint { abortOnError = true; checkReleaseBuilds = true; disable += setOf("GradleDependency", "OldTargetApi", "SetTextI18n") }
}

dependencies {
    testImplementation("junit:junit:4.13.2") // test-only, never shipped
    testImplementation("org.json:json:20240303") // the platform's org.json is a stub in JVM unit tests
}
