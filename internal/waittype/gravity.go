package waittype

// Gravity is how much a state needs its user (subject sub_x000000000000042,
// D1): the one table every pigtail pill takes its colour from. A new state
// picks a gravity, never a hex.
type Gravity int

// The five gravities, least to most severe.
const (
	Grey         Gravity = iota // nothing to do
	Blue                        // the agent works on: nothing to do, but alive (not an idle grey)
	Green                       // work ready: look at it, or close it
	Orange                      // your turn to answer
	Red                         // blocked
	GravityCount = 5
)

var gravityColour = [GravityCount]string{
	Grey:   "#8E8E93",
	Blue:   "#0A84FF",
	Green:  "#34C759",
	Orange: "#FF9500",
	Red:    "#FF3B30",
}

// Color is the system colour cmux tints the pill with.
func (g Gravity) Color() string { return gravityColour[g] }
