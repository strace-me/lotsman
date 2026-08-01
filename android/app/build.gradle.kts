plugins {
    alias(libs.plugins.android.application)
    alias(libs.plugins.kotlin.android)
    alias(libs.plugins.kotlin.compose)
}

android {
    namespace = "me.strace.lotsman"
    compileSdk = 35

    defaultConfig {
        applicationId = "me.strace.lotsman"
        minSdk = 24
        targetSdk = 35
        versionCode = 1
        versionName = "0.1.0-stage1"

        // gomobile emits four ABIs. Keep the two real-device ones plus x86_64 for
        // the emulator; drop x86 (no modern emulator image needs it) so the APK
        // does not carry a fourth copy of the embedded sing-box.
        ndk {
            abiFilters += listOf("arm64-v8a", "armeabi-v7a", "x86_64")
        }
    }

    buildTypes {
        release {
            // R8 off for Stage 1: the gomobile bridge classes are reached only from
            // JNI, so shrinking them needs keep rules we have not written or tested
            // yet (proguard-rules.pro has the starting set). Turning this on before
            // a device test would produce a NoSuchMethodError at the JNI boundary.
            isMinifyEnabled = false
            proguardFiles(getDefaultProguardFile("proguard-android-optimize.txt"), "proguard-rules.pro")
        }
    }

    compileOptions {
        sourceCompatibility = JavaVersion.VERSION_17
        targetCompatibility = JavaVersion.VERSION_17
    }

    kotlinOptions {
        jvmTarget = "17"
    }

    buildFeatures {
        compose = true
    }

    packaging {
        resources {
            excludes += "/META-INF/{AL2.0,LGPL2.1}"
        }
        jniLibs {
            // The gomobile .so must stay uncompressed and page-aligned; this is the
            // default on API 23+, stated explicitly because a wrong value here fails
            // only at dlopen() time on device.
            useLegacyPackaging = false
        }
    }
}

dependencies {
    // --- the Go side -------------------------------------------------------
    // ONE aar produced by ONE `gomobile bind` invocation that binds BOTH
    // github.com/sagernet/sing-box/experimental/libbox AND our facade package.
    // Resolved through the flatDir repository declared in settings.gradle.kts.
    implementation(group = "", name = "liblotsman", ext = "aar")

    // --- Android -----------------------------------------------------------
    implementation(libs.androidx.core.ktx)
    implementation(libs.androidx.activity.compose)
    implementation(libs.androidx.lifecycle.runtime.ktx)
    implementation(libs.androidx.lifecycle.runtime.compose)

    implementation(platform(libs.androidx.compose.bom))
    implementation(libs.androidx.compose.ui)
    implementation(libs.androidx.compose.ui.graphics)
    implementation(libs.androidx.compose.ui.tooling.preview)
    implementation(libs.androidx.compose.material3)
    debugImplementation(libs.androidx.compose.ui.tooling)
}
