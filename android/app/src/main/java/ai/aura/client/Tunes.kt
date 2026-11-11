package ai.aura.client

/**
 * Which songs of the phone's own music library answer a request (ADR-0061). The library never leaves the phone:
 * AURA's reply only carries what was asked for, and the match is made here. Pure logic, unit-tested.
 */
object Tunes {
    data class Song(val id: Long, val title: String, val artist: String, val uri: String, val durationMs: Long = 0)

    /** [matched] is false when nothing in the library fits the request; [songs] is then the library itself. */
    data class Pick(val songs: List<Song>, val matched: Boolean)

    const val MAX_QUEUE = 30

    /** Words of a request that say "play music" and nothing about which music. */
    private val FILLER = setOf("lagu", "lagunya", "musik", "music", "song", "songs", "putar", "putarkan", "mainkan", "play", "setel", "nyalakan",
        "yang", "dong", "tolong", "saya", "aku", "untuk", "dari", "dan", "the", "a", "of", "by", "apa", "saja", "apapun", "bebas", "terserah",
        "random", "acak", "semua", "ada", "di", "ponsel", "hp", "ini", "itu", "sebuah", "beberapa", "daftar", "putarlah", "any")

    private fun words(s: String): List<String> = s.lowercase().split(Regex("[^\\p{L}\\p{N}]+")).filter { it.length > 1 }

    /**
     * The queue for a request: [query] is what the owner asked for, [wanted] the titles and artists AURA named.
     * A request that names nothing in particular gets the library, newest first.
     */
    fun pick(library: List<Song>, query: String, wanted: List<Pair<String, String>>): Pick {
        val asked = words(query).filter { it !in FILLER }
        val named = wanted.map { words(it.first) to words(it.second) }.filter { it.first.isNotEmpty() || it.second.isNotEmpty() }
        if (asked.isEmpty() && named.isEmpty()) return Pick(library.take(MAX_QUEUE), library.isNotEmpty())
        val scored = library.map { song ->
            val title = words(song.title).toSet()
            val artist = words(song.artist).toSet()
            var score = 0
            for (w in asked) { if (w in title) score += 2; if (w in artist) score += 2 }
            for ((t, a) in named) {
                if (t.isNotEmpty() && title.containsAll(t)) score += 4
                if (a.isNotEmpty() && artist.containsAll(a)) score += 2
            }
            song to score
        }.filter { it.second > 0 }.sortedByDescending { it.second }
        return if (scored.isEmpty()) Pick(library.take(MAX_QUEUE), false) else Pick(scored.map { it.first }.take(MAX_QUEUE), true)
    }

    fun length(ms: Long): String = if (ms <= 0) "" else "%d:%02d".format(ms / 60000, ms / 1000 % 60)
}
