// Root build file. Plugins are declared here with `apply false` so the version
// catalog pins one version for the whole build; :app applies them.
plugins {
    alias(libs.plugins.android.application) apply false
    alias(libs.plugins.kotlin.android) apply false
    alias(libs.plugins.kotlin.compose) apply false
}
