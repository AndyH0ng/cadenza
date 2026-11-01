package discord

// ActivityTypeListening shows the activity as "Listening to …".
const ActivityTypeListening = 2

// Status display types select what replaces "…" in "Listening to …"
// in the member list and status.
const (
	StatusDisplayName    = 0 // application name
	StatusDisplayState   = 1 // Activity.State
	StatusDisplayDetails = 2 // Activity.Details
)

// MaxTextLen and MinTextLen bound every text field, in characters.
const (
	MaxTextLen = 128
	MinTextLen = 2
)

// Button limits. Discord shows at most two buttons, and not to the user
// who set them.
const (
	MaxButtons        = 2
	MaxButtonLabelLen = 32
	MaxButtonURLLen   = 512
)

// Activity is the Rich Presence payload of a SET_ACTIVITY command.
type Activity struct {
	Type              int         `json:"type"`
	StatusDisplayType int         `json:"status_display_type"`
	Details           string      `json:"details,omitempty"`
	State             string      `json:"state,omitempty"`
	Assets            *Assets     `json:"assets,omitempty"`
	Timestamps        *Timestamps `json:"timestamps,omitempty"`
	Buttons           []Button    `json:"buttons,omitempty"`
}

// Button is a link shown under the activity.
type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

// Assets holds image URLs and their hover texts.
type Assets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
	SmallImage string `json:"small_image,omitempty"` // badge over the large image
	SmallText  string `json:"small_text,omitempty"`
	SmallURL   string `json:"small_url,omitempty"` // opened by clicking the badge
}

// Timestamps are Unix milliseconds; together they draw a progress bar.
type Timestamps struct {
	Start int64 `json:"start"`
	End   int64 `json:"end"`
}
