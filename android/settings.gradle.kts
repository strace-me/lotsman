pluginManagement {
    repositories {
        google {
            content {
                includeGroupByRegex("com\\.android.*")
                includeGroupByRegex("com\\.google.*")
                includeGroupByRegex("androidx.*")
            }
        }
        mavenCentral()
        gradlePluginPortal()
    }
}

dependencyResolutionManagement {
    repositoriesMode.set(RepositoriesMode.FAIL_ON_PROJECT_REPOS)
    repositories {
        google()
        mavenCentral()

        // liblotsman.aar is produced by `gomobile bind` (see ../README.md) and dropped
        // into app/libs/ by hand or by the build script. It is deliberately NOT vendored
        // into the repo: it is ~60-120 MB of JNI .so per ABI and is reproducible from
        // the Go tree. A clean checkout will fail to resolve it until you build it once.
        flatDir { dirs("$settingsDir/app/libs") }
    }
}

rootProject.name = "Lotsman"
include(":app")
