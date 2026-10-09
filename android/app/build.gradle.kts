plugins {
    id("com.android.application")
    id("org.jetbrains.kotlin.android")
    id("org.jetbrains.kotlin.plugin.serialization")
}

android {
    namespace = "ai.obvious.mcptt"
    compileSdk = 34

    defaultConfig {
        applicationId = "ai.obvious.mcptt"
        minSdk = 26
        targetSdk = 34
        versionCode = 1
        versionName = "0.1.0"
        // Demo default: the sandbox server the smoke test runs against.
        buildConfigField("String", "DEFAULT_SERVER_URL", "\"http://10.0.2.2:8080\"")
    }

    buildTypes {
        release {
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    buildFeatures {
        viewBinding = true
        buildConfig = true
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    testOptions {
        unitTests.isReturnDefaultValues = true
    }
}

// Forward the live-server gate to the test JVM: without it, -D flags set on
// the Gradle JVM never reach tests (see LiveServerSmokeTest). Forward ONLY
// when present — setting a null value materializes an empty string in the
// test JVM and defeats the test's own skip guard.
val liveServer: String? = System.getProperty("mcptt.liveServer")
tasks.withType<Test>().configureEach {
    if (liveServer != null) {
        systemProperty("mcptt.liveServer", liveServer)
    }
}

dependencies {
    // Signaling + REST.
    implementation("com.squareup.okhttp3:okhttp:4.12.0")
    implementation("org.jetbrains.kotlinx:kotlinx-serialization-json:1.7.3")
    implementation("org.jetbrains.kotlinx:kotlinx-coroutines-android:1.8.1")

    // WebRTC: prebuilt org.webrtc AAR from the Stream WebRTC releases (spec's
    // declared stack). Package `org.webrtc`, same API surface as upstream.
    implementation("io.getstream:stream-webrtc-android:1.3.10")

    // UI: classic Views + Material3, dark operations aesthetic.
    implementation("androidx.core:core-ktx:1.13.1")
    implementation("androidx.appcompat:appcompat:1.7.0")
    implementation("com.google.android.material:material:1.12.0")
    implementation("androidx.constraintlayout:constraintlayout:2.1.4")
    implementation("androidx.lifecycle:lifecycle-service:2.8.5")

    // JVM unit tests for the protocol/state layers.
    testImplementation("junit:junit:4.13.2")
    testImplementation("org.jetbrains.kotlinx:kotlinx-coroutines-test:1.8.1")
    testImplementation("com.squareup.okhttp3:mockwebserver:4.12.0")
}
