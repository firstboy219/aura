package ai.aura.client

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/** Settings; the member token is encrypted with a non-exportable Android Keystore key (AES-GCM). */
class SecureStore(ctx: Context) {
    private val prefs = ctx.getSharedPreferences("aura", Context.MODE_PRIVATE)

    var server: String
        get() = prefs.getString("server", "https://aurax.cosger.online") ?: ""
        set(v) = prefs.edit().putString("server", v).apply()
    var username: String
        get() = prefs.getString("username", "") ?: ""
        set(v) = prefs.edit().putString("username", v).apply()
    var micAllowed: Boolean
        get() = prefs.getBoolean("mic_allowed", false) // off until the owner turns it on
        set(v) = prefs.edit().putBoolean("mic_allowed", v).apply()
    var speakResults: Boolean
        get() = prefs.getBoolean("speak", false)
        set(v) = prefs.edit().putBoolean("speak", v).apply()
    var backgroundChecks: Boolean
        get() = prefs.getBoolean("bg", false)
        set(v) = prefs.edit().putBoolean("bg", v).apply()
    /** Words learned from AURA's corrections of what the recognizer heard (names, places, terms); newest first. */
    var vocabulary: List<String>
        get() = prefs.getString("vocabulary", "")?.split("\n")?.filter { it.isNotBlank() } ?: emptyList()
        set(v) = prefs.edit().putString("vocabulary", v.take(100).joinToString("\n")).apply()
    var seenNotices: Set<String>
        get() = prefs.getStringSet("seen", emptySet()) ?: emptySet()
        set(v) = prefs.edit().putStringSet("seen", v.toList().takeLast(200).toSet()).apply()

    var token: String
        get() {
            val enc = prefs.getString("token", null) ?: return ""
            return try {
                val b = Base64.decode(enc, Base64.NO_WRAP)
                val c = Cipher.getInstance("AES/GCM/NoPadding")
                c.init(Cipher.DECRYPT_MODE, key(), GCMParameterSpec(128, b, 0, 12))
                String(c.doFinal(b, 12, b.size - 12))
            } catch (e: Exception) {
                ""
            }
        }
        set(v) {
            if (v.isEmpty()) { prefs.edit().remove("token").apply(); return }
            val c = Cipher.getInstance("AES/GCM/NoPadding")
            c.init(Cipher.ENCRYPT_MODE, key())
            val out = c.iv + c.doFinal(v.toByteArray())
            prefs.edit().putString("token", Base64.encodeToString(out, Base64.NO_WRAP)).apply()
        }

    private fun key(): SecretKey {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (ks.getKey(ALIAS, null) as? SecretKey)?.let { return it }
        val g = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        g.init(KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
            .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build())
        return g.generateKey()
    }

    private companion object { const val ALIAS = "aura_token_key" }
}
