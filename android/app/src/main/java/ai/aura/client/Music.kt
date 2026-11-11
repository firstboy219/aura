package ai.aura.client

import android.content.ContentUris
import android.content.Context
import android.media.AudioAttributes
import android.media.MediaPlayer
import android.net.Uri
import android.provider.MediaStore

/**
 * Plays the phone's own music (ADR-0061). It reads the list of songs only after the owner granted access, and plays
 * only items of the system media library; nothing is downloaded and the list is not sent anywhere.
 */
class Music(private val ctx: Context) {
    private var player: MediaPlayer? = null
    private var wanted = false

    fun library(): List<Tunes.Song> {
        val out = mutableListOf<Tunes.Song>()
        val base = MediaStore.Audio.Media.EXTERNAL_CONTENT_URI
        val cols = arrayOf(MediaStore.Audio.Media._ID, MediaStore.Audio.Media.TITLE, MediaStore.Audio.Media.ARTIST, MediaStore.Audio.Media.DURATION)
        ctx.contentResolver.query(base, cols, MediaStore.Audio.Media.IS_MUSIC + " != 0", null, MediaStore.Audio.Media.DATE_ADDED + " DESC")?.use { c ->
            while (c.moveToNext() && out.size < MAX_SONGS) {
                val id = c.getLong(0)
                val artist = c.getString(2).orEmpty().takeIf { it != MediaStore.UNKNOWN_STRING }.orEmpty()
                out += Tunes.Song(id, c.getString(1).orEmpty(), artist, ContentUris.withAppendedId(base, id).toString(), c.getLong(3))
            }
        }
        return out
    }

    fun play(uri: String, done: () -> Unit, failed: (String) -> Unit) {
        stop()
        if (!uri.startsWith("content://media/")) { failed("Bukan lagu dari koleksi ponsel"); return }
        try {
            val p = MediaPlayer()
            p.setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).setContentType(AudioAttributes.CONTENT_TYPE_MUSIC).build())
            p.setDataSource(ctx, Uri.parse(uri))
            p.setOnPreparedListener { if (wanted && player === it) it.start() }
            p.setOnCompletionListener { if (player === it) done() }
            p.setOnErrorListener { it, _, _ -> if (player === it) { stop(); failed("Lagu ini tidak bisa diputar") }; true }
            wanted = true
            player = p
            p.prepareAsync()
        } catch (e: Exception) {
            stop()
            failed("Lagu ini tidak bisa diputar")
        }
    }

    fun pause() { wanted = false; runCatching { player?.takeIf { it.isPlaying }?.pause() } }

    fun resume() { wanted = true; runCatching { player?.start() } }

    fun stop() {
        wanted = false
        player?.let { p -> runCatching { p.reset() }; runCatching { p.release() } }
        player = null
    }

    private companion object { const val MAX_SONGS = 3000 }
}
