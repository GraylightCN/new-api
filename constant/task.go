package constant

type TaskPlatform string

const (
	TaskPlatformSuno       TaskPlatform = "suno"
	TaskPlatformMidjourney              = "mj"
	// TaskPlatformVolcNative marks native Volc Ark video tasks. These reuse the
	// existing VolcEngine(45) channel rather than a dedicated channel type; the
	// named platform (not the channel-type number) selects our task adaptor at
	// both submit and poll time, so it is set on the route context and persisted
	// on the task row.
	TaskPlatformVolcNative TaskPlatform = "volc-native"
)

const (
	SunoActionMusic  = "MUSIC"
	SunoActionLyrics = "LYRICS"

	TaskActionGenerate          = "generate"
	TaskActionTextGenerate      = "textGenerate"
	TaskActionFirstTailGenerate = "firstTailGenerate"
	TaskActionReferenceGenerate = "referenceGenerate"
	TaskActionRemix             = "remixGenerate"
)

var SunoModel2Action = map[string]string{
	"suno_music":  SunoActionMusic,
	"suno_lyrics": SunoActionLyrics,
}
