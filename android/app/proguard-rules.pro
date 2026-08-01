# Starting keep rules for the gomobile bridge. UNVERIFIED — release builds have
# isMinifyEnabled=false for Stage 1, so none of this has been exercised.
#
# Everything gobind generates is reached from native code (JNI looks classes and
# methods up by name), so R8 cannot see the references and will happily strip
# them. Same for our own classes that Go calls back into: the PlatformInterface
# and EventSink implementations.

-keep class me.strace.libbox.** { *; }
-keep class me.strace.mobile.** { *; }
-keep class go.** { *; }

-keep class me.strace.lotsman.LotsmanVpnService { *; }
